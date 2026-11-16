// examples：log 组件的可编译示例——独立于根包的 package examples，不参与
// 组件公开 API；go build ./... 会编译本目录，保证示例与真实 API 同步不腐化。
// 函数体仅在有人显式调用时产生输出，这里以可读性优先，供复制改造。
//
// examples: compilable samples of the log component — a package examples
// separate from the root package and outside its public API; go build ./...
// compiles this directory so the samples stay in sync with the real API and
// cannot rot. The function bodies only produce output when explicitly called;
// readability comes first, they are meant to be copied and adapted.
package examples

import (
	"os"

	"github.com/goal-web/contracts"
	"github.com/goal-web/log"
)

// StandaloneUsage 演示不经容器直接使用：ParseConfig 解析原始配置
// （contracts.Fields，与 TOML [log] 节同构），NewFactory 创建 LogManager。
// 配置为 console + file + stack 三通道，stack 经 zapcore.NewTee 把同一条日志
// 扇出到 console 与 file。配置完全为空时 NewFactory 自动注入内置
// console/debug 通道兜底。
//
// StandaloneUsage demonstrates standalone usage without a container:
// ParseConfig parses the raw configuration (contracts.Fields, shaped like the
// TOML [log] section) and NewFactory creates the LogManager. The
// configuration is the console + file + stack trio; stack fans one record out
// to console and file via zapcore.NewTee. An entirely empty configuration
// gets the built-in console/debug channel injected as the fallback.
func StandaloneUsage() contracts.LogManager {
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
				log.KeyPath:   "storage/logs/app.log", // 按天切分为 app-20261117.log
			},
			"stack": contracts.Fields{
				log.KeyDriver:   "stack",
				log.KeyLevel:    "debug",
				log.KeyChannels: []string{"console", "file"},
			},
		},
	}))

	// 默认通道（stack）：一条日志同时进 console 与 file。
	// The default channel (stack): one record reaches console and file alike.
	factory.Channel().WithField("module", "examples").Info("hello goal-web log")

	// 指定通道：只写文件。
	// A named channel: file only.
	factory.Channel("file").Error("persisted error")

	return factory
}

// RegisterCustomDriver 演示运行时注册自定义驱动：Extend 注册 "memory" 驱动，
// NewBufferLogger 把日志写入任意 io.Writer（这里用 stdout；测试里常用
// bytes.Buffer）。注册后配置里 driver = "memory" 的通道即可使用；"stack"
// 为保留驱动名，注册同名驱动不会改变其组合构建行为。
//
// RegisterCustomDriver demonstrates runtime custom-driver registration:
// Extend registers a "memory" driver, with NewBufferLogger writing to any
// io.Writer (stdout here; tests typically use a bytes.Buffer). Once
// registered, any channel with driver = "memory" uses it; "stack" is the
// reserved driver name — registering under it does not change the composition
// behavior.
func RegisterCustomDriver(manager contracts.LogManager) contracts.LogManager {
	manager.Extend("memory", func(channelName string, cfg contracts.Fields) (contracts.Logger, error) {
		// writer 由驱动提供；level/format 等其余配置沿用通道配置。
		// The driver supplies the writer; level/format and the rest follow the
		// channel config.
		fields := contracts.Fields{log.KeyWriter: os.Stdout}
		for key, value := range cfg {
			fields[key] = value
		}

		return log.NewBufferLogger(fields)
	})

	return manager
}
