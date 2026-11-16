// config.go：日志组件的正式配置结构与原始配置解析。
//
// config.go: the formal configuration structure of the log component and
// parsing of raw configuration.
package log

import (
	"github.com/goal-web/contracts"
)

// 配置键常量：配置解析与驱动实现统一引用，避免内联魔法字符串。
// Config key constants: referenced uniformly by config parsing and driver
// implementations to avoid inline magic strings.
const (
	// KeyDefault 是顶层配置中默认通道名的键。
	// KeyDefault is the top-level key holding the default channel name.
	KeyDefault = "default"
	// KeyChannels 是顶层配置中通道表（键为通道名，值为通道配置）的键；
	// 同名键也用于 stack 通道配置中的子通道名列表（[]string 或 []any）。
	// KeyChannels is the top-level key holding the channel table (channel
	// name -> channel config); the same key holds a stack channel's child-name
	// list ([]string or []any) inside its channel config.
	KeyChannels = "channels"
	// KeyDriver 是通道配置中驱动名的键。
	// KeyDriver is the channel key holding the driver name.
	KeyDriver = "driver"
	// KeyLevel 是通道配置中日志级别的键。
	// KeyLevel is the channel key holding the log level.
	KeyLevel = "level"
	// KeyFormat 是通道配置中输出格式的键。
	// KeyFormat is the channel key holding the output format.
	KeyFormat = "format"
	// KeyWriter 是通道配置中日志输出目标的键（io.Writer）。
	// KeyWriter is the channel key holding the log output target (io.Writer).
	KeyWriter = "writer"
	// KeyPath 是通道配置中日志文件路径的键。
	// KeyPath is the channel key holding the log file path.
	KeyPath = "path"
	// KeyOutput 是通道配置中输出通道（stdout/stderr）的键。
	// KeyOutput is the channel key holding the output stream (stdout/stderr).
	KeyOutput = "output"
)

// Config 保存日志组件配置。
// Config holds the log component configuration.
type Config struct {
	// Default 指定默认日志通道。
	// Default specifies the default log channel.
	Default string
	// Channels 包含各通道的配置。
	// Channels contains per-channel configurations.
	Channels map[string]contracts.Fields
}

// ParseConfig 将任意原始配置解析为 Config。接受两种合法形态：
//   - log.Config：强类型直通，原样返回（goal 骨架的 ConfigProvider 惯例返回
//     结构体，参照 goal/config/cache.go 返回 cache.Config）；不复制内部 map，
//     返回后同样不得再改动（见 NewFactory 的约束说明）；
//   - contracts.Fields：与 TOML [log] 节同构的原始形态，按下列规则解析。
//
// Fields 形态的解析规则：
//   - default 键缺失或值不是 string：Default 保持空串；
//   - channels 键缺失或类型不是 contracts.Fields：视为缺失，返回空 map；
//   - 单个通道条目类型不是 contracts.Fields：跳过该条目，不影响其余通道。
//
// 其余形态（含 nil）返回零配置，由工厂应用兜底。
// ParseConfig 不注入兜底配置——零配置兜底是工厂（NewFactory）的职责。
//
// ParseConfig parses arbitrary raw configuration into a Config. Two shapes
// are accepted:
//   - log.Config: strongly-typed passthrough, returned as-is (the customary
//     return shape of the goal skeleton's ConfigProvider, cf.
//     goal/config/cache.go returning cache.Config); inner maps are not
//     copied and must not be mutated afterwards either (see NewFactory);
//   - contracts.Fields: the raw shape mirroring the TOML [log] section,
//     parsed per the rules below.
//
// Parsing rules for the Fields shape:
//   - a missing or non-string default key leaves Default empty;
//   - a missing or mistyped channels key is treated as missing (empty map);
//   - a channel entry that is not contracts.Fields is skipped without
//     affecting the other channels.
//
// Any other shape (including nil) yields the zero Config, leaving fallback
// handling to the factory. ParseConfig never injects fallbacks itself —
// zero-config fallback is the factory's (NewFactory) responsibility.
func ParseConfig(raw any) Config {
	switch value := raw.(type) {
	case Config:
		// 强类型直通：与 Fields 路径存引用的语义一致，共享内部 map 不拷贝；
		// 调用方后续同样不得改动（工厂据此静态快照计算 stack 闭包）。
		// Strongly-typed passthrough: consistent with the Fields path storing
		// references, inner maps are shared un-copied; the caller must not
		// mutate them afterwards either (the factory computes its static stack
		// closure from this snapshot).
		return value
	case contracts.Fields:
		config := Config{
			Channels: map[string]contracts.Fields{},
		}

		if defaultName, ok := value[KeyDefault].(string); ok {
			config.Default = defaultName
		}

		channels, ok := value[KeyChannels].(contracts.Fields)
		if !ok {
			return config
		}

		for name, rawChannel := range channels {
			channelConfig, ok := rawChannel.(contracts.Fields)
			if !ok {
				continue
			}
			config.Channels[name] = channelConfig
		}

		return config
	default:
		return Config{}
	}
}
