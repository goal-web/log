# goal-web/log

Goal-Web 框架的日志组件，基于 [zap](https://github.com/uber-go/zap) 实现多通道日志，采用 Laravel 风格的通道配置。

[English](./README.md)

## 特性

- **内置驱动**：`console`（stdout/stderr）与 `file`（按天切分），外加保留的 `stack` 组合驱动
- **按天切分**：`path = "storage/logs/app.log"` 落盘为 `app-20261117.log`，一天一个文件，不引入轮转依赖
- **自定义驱动**：运行时经 `Extend` 注册；`NewBufferLogger` 把日志构建到任意 `io.Writer` 上
- **零配置兜底**：完全空配置自动生成内置 `console/debug` 通道；任何通道构建失败降级为 `stderr/debug/console` 兜底 logger 并告警
- **每通道级别过滤**：低于通道级别的日志在格式化前短路，零格式化开销
- **并发安全构建**：首次构建按 singleflight 合并，stack 通道带静态环检测与死锁防护
- **Laravel 风格配置**：一个 `[log]` 配置节、命名通道、`stack` 扇出

## 安装

```bash
go get github.com/goal-web/log@latest
```

要求 Go >= 1.25.0，依赖 [github.com/goal-web/contracts](https://github.com/goal-web/contracts)（v0.5.7+）与 [zap](https://github.com/uber-go/zap)。

## 快速开始

### 配置

```toml
[log]
default = "stack"

[log.channels.console]
driver = "console"             # console | file | stack | 自定义
level = "debug"                # debug | info | warn | error | fatal
format = "console"             # console | json

[log.channels.file]
driver = "file"
level = "info"
format = "json"
path = "storage/logs/app.log"  # 按天切分：app-20261117.log

[log.channels.stack]
driver = "stack"
level = "debug"
channels = ["console", "file"] # 扇出目标
```

通道首次访问时懒构建并缓存；`stack` 通道经 `zapcore.NewTee` 把同一条日志扇出到所有子通道，自身级别在扇出后统一过滤。上面的 `[log]` 节展示的是配置模型；容器路径下它需要经注册的 config provider 才能到达组件——见下一节的接线说明。

### 容器内使用

注册服务，之后在容器可解析的任意位置依赖注入 `contracts.LogManager`。config providers 里必须有 `"log"` 键（接线说明见下方），否则 `config.Get("log")` 为 nil，组件静默走零配置兜底：

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
			map[string]contracts.ConfigProvider{
				"log": func(env contracts.Env) any {
					// 与上方 [log] TOML 节同构
					return contracts.Fields{
						"default": "stack",
						"channels": contracts.Fields{
							"console": contracts.Fields{"driver": "console", "level": "debug", "format": "console"},
							"file":    contracts.Fields{"driver": "file", "level": "info", "format": "json", "path": "storage/logs/app.log"},
							"stack":   contracts.Fields{"driver": "stack", "level": "debug", "channels": []string{"console", "file"}},
						},
					}
				},
			},
		),
		log.NewService(),
	)

	app.Call(func(logger contracts.LogManager) {
		logger.Channel().WithField("order_id", 1).Info("查询订单") // 默认通道
		logger.Channel("file").Error("只写文件")
	})
}
```

服务提供者从 `config.Get("log")` 构建 `"log"` 单例；取值为空或形态非法时兜底到内置 console 通道，应用照常启动。

#### 为什么必须注册 provider

config 组件把 env.toml 压平为点分键存储（`log.default`、`log.channels.console.driver`、……），而 `config.Get("log")` 查的是精确键 `"log"`。只在 env.toml 写 `[log]` 节而不注册 `"log"` provider 时，`config.Get("log")` 为 nil：组件静默走零配置兜底（console/debug），`file`/`stack` 通道完全不生效，且没有任何警告。

goal 骨架经 `init()` 模式统一注册 config provider（见 `goal/config/cache.go`）；log provider 照同样形态注册，返回值可以是上面的 `contracts.Fields`，也可以是强类型 `log.Config`（`ParseConfig` 原样直通）：

```go
// goal/config/log.go —— goal/config/*.go 通用的 init() 注册模式
func init() {
	configs["log"] = func(env contracts.Env) any {
		return log.Config{
			Default: "stack",
			Channels: map[string]contracts.Fields{
				"console": {log.KeyDriver: "console", log.KeyLevel: "debug", log.KeyFormat: "console"},
				"file":    {log.KeyDriver: "file", log.KeyLevel: "info", log.KeyFormat: "json", log.KeyPath: "storage/logs/app.log"},
				"stack":   {log.KeyDriver: "stack", log.KeyLevel: "debug", log.KeyChannels: []string{"console", "file"}},
			},
		}
	}
}
```

取值同样可以来自环境变量（`env.GetString(...)`），如 `goal/config/cache.go` 的做法。`ParseConfig` 只接受两种形态：`log.Config`（强类型直通）与 `contracts.Fields`（与 TOML `[log]` 节同构）；其余形态走零配置兜底。

### 独立使用（不经容器）

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

factory.Channel().WithField("module", "payment").Info("订单已支付") // stack 扇出
factory.Channel("console").Warn("只进 console")
```

独立用法的可运行版本见 [`examples/logging.go`](./examples/logging.go)（`StandaloneUsage`，自定义驱动流程见 `RegisterCustomDriver`）。

### 输出格式

两种格式共用同一字段集：`time`（`2006-01-02 15:04:05`）、大写 `level`、通道名（`channel` 键）与 `msg`；json 格式编码为单个对象。`Fatal` 写完日志后以状态码 1 退出进程（zap 语义）。

## 自定义驱动

`Extend` 以名称注册驱动，配置里 `driver = "<名称>"` 的通道即可使用；`NewBufferLogger` 是把日志构建到任意 `io.Writer` 的标准入口（普通 writer 自动加锁，可安全共享）：

```go
factory.Extend("memory", func(channelName string, cfg contracts.Fields) (contracts.Logger, error) {
	// writer 由驱动提供；level/format 沿用通道配置
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
factory.Channel("memory").Info("走自定义驱动")
```

`NewBufferLogger` 也常用于测试：

```go
var buf bytes.Buffer
logger, err := log.NewBufferLogger(contracts.Fields{
	log.KeyWriter: &buf,
	log.KeyLevel:  "debug",
	log.KeyFormat: "console",
})
```

注意：`"stack"` 为保留驱动名——注册同名驱动只会覆盖驱动表条目，构建行为仍走 stack 组合分支。

## 性能

[`tests/benchmark_test.go`](./tests/benchmark_test.go) 在同等工作负载下对比 zap 通道与原 apex/log 包装的单条写日志开销：相同固定消息、1 个字段、Info 级别（两侧级别过滤均放行）。两侧都不写真实终端——zap 通道用 console 编码器写 `io.Discard`，apex 侧走官方 `handlers/text`（console 格式的对应物，含真实格式化）写 `io.Discard`。刻意不用纯 no-op 的 `handlers/discard`：它跳过格式化，测出的就不是「写一条日志」的真实成本。

| 基准                          | ns/op（中位数） | B/op | allocs/op |
|-------------------------------|---------------:|-----:|----------:|
| `BenchmarkZapChannel`（zap）  | 769.6          | 1450 | 11        |
| `BenchmarkApexLogger`（apex） | 730.5          | 960  | 11        |

两条管线同一量级：zap 通道相比 apex `text` handler 每条约多 5% 延迟、约 490 B 内存——这是结构化 console 编码（时间布局、级别、通道名）的代价。数字随硬件与负载变化，仅示意量级；console 编码含格式化成本，生产管线也可选用 `json` 格式。测试环境：Apple M1 Pro（darwin/arm64）、Go 1.25.4、`-count=5` 取中位数。

复现命令：

```bash
go test ./tests -bench 'BenchmarkZapChannel|BenchmarkApexLogger' -benchmem -run '^$' -count=5
```

## 零配置兜底行为

- **空配置**：`NewFactory` 在无默认通道且无通道表时注入内置 `console/debug/console` 通道（名为 `console`），`log.NewFactory(log.Config{}).Channel()` 开箱即用。
- **通道构建失败**：通道未配置、驱动未注册，或构建失败（级别/格式非法、`path` 缺失、stack 环引用或子通道非 zap 实现）时，返回兜底 logger 而非报错。构建 panic 会撤销登记并以兜底 logger 放行等待方，一次 panic 不会卡死后续请求。兜底走正式 console 管线（stderr、debug 级别），输出中名为 `fallback`，并携带原因 `Warn` 一次（兜底结果进缓存，告警不重复刷屏）。

## 约束说明

1. **`NewFactory` 之后不得再修改传入的 `Config`**——工厂基于该静态快照计算 stack 依赖闭包做并发构建死锁防护，事后改动 `Config.Channels` 不会被感知。
2. **驱动构建期间不得回调 `LogManager.Channel`**（含间接调用）——回调可能落在一次进行中构建的等待路径上形成互等死锁，且构建内外的环检测都看不见驱动发起的重入。
3. **`stack` 的重复子通道不去重**——同一子通道的 core 在 tee 中出现多次，同一条日志会重复写入（按配置语义直译的双写）。

## 配置键参考

| TOML 键    | 常量             | 作用范围                 | 取值 / 含义                                              |
|------------|------------------|--------------------------|----------------------------------------------------------|
| `default`  | `log.KeyDefault` | `[log]`                  | 默认通道名                                                |
| `channels` | `log.KeyChannels` | `[log]`；stack 通道      | 通道表；stack 通道的子通道名列表                           |
| `driver`   | `log.KeyDriver`  | 通道                     | `console`、`file`、`stack` 或自定义驱动名                  |
| `level`    | `log.KeyLevel`   | 通道                     | `debug`、`info`、`warn`/`warning`、`error`、`fatal`；默认 `debug` |
| `format`   | `log.KeyFormat`  | 通道                     | `console`（默认）、`json`                                 |
| `path`     | `log.KeyPath`    | `file` 通道              | 日志原始路径；按天切分为 `前缀-YYYYMMDD.后缀`              |
| `output`   | `log.KeyOutput`  | `console` 通道           | `stdout`（默认）、`stderr`                                |
| `writer`   | `log.KeyWriter`  | 仅 `NewBufferLogger`     | `io.Writer`；不是 TOML 键                                 |

`level` 与 `format` 不区分大小写；字段缺失走默认值，存在但非法则导致通道构建失败（见上方兜底行为）。本表与 godoc 不一致时，以 godoc 为准。

## 开源协议

MIT
