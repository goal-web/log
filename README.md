# goal-web/log

A multi-channel logging component for the Goal-Web framework, powered by [zap](https://github.com/uber-go/zap), with Laravel-style channel configuration.

[中文文档](./README.cn.md)

## Features

- **Built-in drivers**: `console` (stdout/stderr) and `file` (daily-split), plus the reserved `stack` composition driver
- **Daily file rotation**: `path = "storage/logs/app.log"` lands as `app-20261117.log`, one file per day, no rotation dependency
- **Custom drivers**: register at runtime with `Extend`; `NewBufferLogger` builds a logger onto any `io.Writer`
- **Zero-config fallback**: an empty configuration yields a built-in `console/debug` channel; any channel build failure degrades to a `stderr/debug/console` fallback logger with a warning
- **Per-channel level filtering**: entries below a channel's level short-circuit before any formatting cost
- **Concurrency-safe channel building**: first builds are coalesced singleflight-style, with static cycle detection and deadlock protection for `stack` channels
- **Laravel-style configuration**: one `[log]` section, named channels, `stack` fan-out

## Installation

```bash
go get github.com/goal-web/log@latest
```

Requires Go >= 1.25.0. Depends on [github.com/goal-web/contracts](https://github.com/goal-web/contracts) (v0.5.7+) and [zap](https://github.com/uber-go/zap).

## Quick Start

### Configuration

```toml
[log]
default = "stack"

[log.channels.console]
driver = "console"             # console | file | stack | custom
level = "debug"                # debug | info | warn | error | fatal
format = "console"             # console | json

[log.channels.file]
driver = "file"
level = "info"
format = "json"
path = "storage/logs/app.log"  # daily split: app-20261117.log

[log.channels.stack]
driver = "stack"
level = "debug"
channels = ["console", "file"] # fan-out targets
```

Channels are built lazily on first use and cached; a `stack` channel fans one record out to all of its child channels with `zapcore.NewTee` and applies its own level uniformly after the fan-out.

### Inside the Goal-Web container

Register the service, then depend on `contracts.LogManager` anywhere the container resolves:

```go
package main

import (
	"github.com/goal-web/application"
	"github.com/goal-web/config"
	"github.com/goal-web/contracts"
	"github.com/goal-web/log"
)

func main() {
	app := application.Default()

	app.RegisterServices(
		config.NewService(
			config.NewToml(config.File("env.toml")),
			map[string]contracts.ConfigProvider{},
		),
		log.NewService(),
	)

	app.Call(func(logger contracts.LogManager) {
		logger.Channel().WithField("order_id", 1).Info("order queried") // default channel
		logger.Channel("file").Error("written to file only")
	})
}
```

The service provider binds the `"log"` singleton from the `[log]` config section (`config.Get("log")`); a missing or malformed section falls back to the built-in console channel, so the application always starts.

### Standalone usage (no container)

```go
factory := log.NewFactory(log.ParseConfig(contracts.Fields{
	log.KeyDefault: "stack",
	log.KeyChannels: contracts.Fields{
		"console": contracts.Fields{
			log.KeyDriver: "console",
			log.KeyLevel:  "debug",
			log.KeyFormat: "console",
		},
		"file": contracts.Fields{
			log.KeyDriver: "file",
			log.KeyLevel:  "info",
			log.KeyFormat: "json",
			log.KeyPath:   "storage/logs/app.log",
		},
		"stack": contracts.Fields{
			log.KeyDriver:   "stack",
			log.KeyLevel:    "debug",
			log.KeyChannels: []string{"console", "file"},
		},
	},
}))

factory.Channel().WithField("module", "payment").Info("order paid") // stack fan-out
factory.Channel("console").Warn("console only")
```

A runnable version of both usages lives in [`examples/logging.go`](./examples/logging.go).

### Output formats

Both formats share the same field set: `time` (`2006-01-02 15:04:05`), capital `level`, the channel name under `channel`, and `msg`; JSON encodes them as a single object. `Fatal` writes the record and exits the process with status 1 (zap semantics).

## Custom Drivers

`Extend` registers a driver under a name; any channel configured with `driver = "<name>"` uses it. `NewBufferLogger` is the standard way to build a logger onto any `io.Writer` (it locks plain writers so they are safe to share):

```go
factory.Extend("memory", func(channelName string, cfg contracts.Fields) (contracts.Logger, error) {
	// the driver supplies the writer; level/format follow the channel config
	fields := contracts.Fields{log.KeyWriter: os.Stdout}
	for key, value := range cfg {
		fields[key] = value
	}

	return log.NewBufferLogger(fields)
})
```

```toml
[log.channels.memory]
driver = "memory"
level = "info"
format = "json"
```

```go
factory.Channel("memory").Info("through the custom driver")
```

`NewBufferLogger` is also handy in tests:

```go
var buf bytes.Buffer
logger, err := log.NewBufferLogger(contracts.Fields{
	log.KeyWriter: &buf,
	log.KeyLevel:  "debug",
	log.KeyFormat: "console",
})
```

Note: `"stack"` is a reserved driver name — registering a driver under it only overwrites the driver-table entry; building still routes through the stack composition branch.

## Zero-Configuration Fallback

- **Empty configuration**: `NewFactory` with no default channel and no channel table injects a built-in `console/debug/console` channel named `console`, so `log.NewFactory(log.Config{}).Channel()` works out of the box.
- **Failed channel builds**: a channel that is not configured, names an unregistered driver, or fails to build (invalid level/format, missing `path`, stack errors such as cycles or non-zap children) returns a fallback logger instead of an error. A build that panics retracts itself and releases its waiters with the fallback logger, so one panic cannot wedge later requests. The fallback is the formal console pipeline on stderr at debug level, named `fallback` in the output, and it emits a `Warn` carrying the reason once (the fallback result is cached, so the warning does not repeat).

## Constraints

1. **Do not mutate the `Config` after `NewFactory`** — the factory computes the static stack dependency closure from the snapshot for concurrent-build deadlock protection; later changes to `Config.Channels` go unnoticed.
2. **A driver must not call back into `LogManager.Channel` during its build** (directly or indirectly) — the call can land on the wait path of an in-flight build and deadlock against it, and neither the in-build nor the static cycle detection can see driver-initiated re-entrancy.
3. **Duplicate child channels in a `stack` are not deduplicated** — the child's core appears in the tee multiple times and one record is written to it repeatedly (intentional double-write, taken literally from the configuration).

## Configuration Keys Reference

| TOML key   | Constant         | Scope                    | Values / meaning                                             |
|------------|------------------|--------------------------|--------------------------------------------------------------|
| `default`  | `log.KeyDefault` | `[log]`                  | default channel name                                          |
| `channels` | `log.KeyChannels`| `[log]`; stack channel   | channel table; a stack channel's child-name list              |
| `driver`   | `log.KeyDriver`  | channel                  | `console`, `file`, `stack`, or a custom driver name           |
| `level`    | `log.KeyLevel`   | channel                  | `debug`, `info`, `warn`/`warning`, `error`, `fatal`; default `debug` |
| `format`   | `log.KeyFormat`  | channel                  | `console` (default), `json`                                   |
| `path`     | `log.KeyPath`    | `file` channel           | raw log path; daily split into `prefix-YYYYMMDD.suffix`       |
| `output`   | `log.KeyOutput`  | `console` channel        | `stdout` (default), `stderr`                                  |
| `writer`   | `log.KeyWriter`  | `NewBufferLogger` only   | `io.Writer`; not a TOML key                                   |

`level` and `format` are case-insensitive; a missing field takes its default, while a present-but-invalid one fails the channel build (see the fallback behavior above).

## License

MIT
