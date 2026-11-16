package log

import (
	"fmt"
	"os"
	"sync"

	"github.com/goal-web/contracts"
	"github.com/goal-web/log/drivers"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Factory 是 contracts.LogManager 的实现：按名称懒构建通道并缓存，
// 支持运行时注册自定义驱动，并在零配置或通道缺失时兜底。
// 内置 console/file 驱动的 writer 由 drivers 子包提供，组合在本包完成；
// 自定义驱动的统一收编（类型守卫等）由 Task 6 处理。
// Factory is a contracts.LogManager: channels are built lazily by name and
// cached, custom drivers can be registered at runtime, and missing or empty
// configurations fall back to a built-in channel. Writers of the built-in
// console/file drivers come from the drivers subpackage while composition
// happens in this package; unifying custom drivers (type guards etc.) is
// handled by Task 6.
type Factory struct {
	config   Config
	drivers  map[string]contracts.LogDriver
	channels map[string]contracts.ChannelLogger
	mutex    sync.Mutex
}

// NewFactory 用给定配置创建 LogManager。配置完全为空（无默认通道且无通道表）
// 时注入内置 console/debug 通道，保证开箱即用；通道表可由 ParseConfig 生成。
// NewFactory creates a LogManager with the given configuration. An entirely
// empty configuration (no default channel and no channel table) gets the
// built-in console/debug channel injected so the component works out of the
// box; the channel table may come from ParseConfig.
func NewFactory(config Config) contracts.LogManager {
	if config.Default == "" && len(config.Channels) == 0 {
		config = Config{
			Default: "console",
			Channels: map[string]contracts.Fields{
				"console": {
					KeyDriver: "console",
					KeyLevel:  "debug",
					KeyFormat: "console",
				},
			},
		}
	}

	return &Factory{
		config:   config,
		drivers:  newBuiltinDrivers(),
		channels: map[string]contracts.ChannelLogger{},
	}
}

// newBuiltinDrivers 返回内置驱动表：console（stdout/stderr，output 控制）
// 与 file（按日切分，path 控制）。drivers 子包只提供 writer（不能反向引用
// log 包），writer + encoder + level → logger 的组合在此完成；返回的
// Logger 实际为 ChannelLogger（zap 适配实现），兜底分派见 buildChannel。
// newBuiltinDrivers returns the built-in driver table: console
// (stdout/stderr, via output) and file (daily-split, via path). The drivers
// subpackage provides writers only (it must not import log back), so the
// writer + encoder + level -> logger composition happens here; the returned
// Logger is in fact a ChannelLogger (zap-backed), with the fallback dispatch
// in buildChannel.
func newBuiltinDrivers() map[string]contracts.LogDriver {
	return map[string]contracts.LogDriver{
		"console": func(_ string, cfg contracts.Fields) (contracts.Logger, error) {
			writer, err := drivers.NewConsoleWriter(cfg)
			if err != nil {
				return nil, err
			}

			return newChannelFromWriter(cfg, writer)
		},
		"file": func(_ string, cfg contracts.Fields) (contracts.Logger, error) {
			writer, err := drivers.NewFileWriter(cfg)
			if err != nil {
				return nil, err
			}

			return newChannelFromWriter(cfg, writer)
		},
	}
}

// newChannelFromWriter 用通道配置中的 level 与 format，把 driver 提供的
// writer 组装为 zap 通道 logger，是内置驱动共用的组合路径。
// newChannelFromWriter assembles a writer from a driver into a zap channel
// logger using the level and format from the channel config; it is the shared
// composition path for the built-in drivers.
func newChannelFromWriter(cfg contracts.Fields, writer zapcore.WriteSyncer) (contracts.Logger, error) {
	level, err := configString(cfg, KeyLevel)
	if err != nil {
		return nil, err
	}

	format, err := configString(cfg, KeyFormat)
	if err != nil {
		return nil, err
	}

	return newChannelLogger(writer, level, format)
}

// Extend 以给定名称注册自定义日志驱动。
// Extend registers a custom log driver under the given name.
func (f *Factory) Extend(name string, driver contracts.LogDriver) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	f.drivers[name] = driver
}

// Channel 返回给定通道的 logger；不传名称时返回默认通道。
// Channel returns the logger for the given channel; no name means the default channel.
func (f *Factory) Channel(name ...string) contracts.ChannelLogger {
	channelName := f.config.Default
	if len(name) > 0 && name[0] != "" {
		channelName = name[0]
	}

	f.mutex.Lock()
	defer f.mutex.Unlock()

	if cached, ok := f.channels[channelName]; ok {
		return cached
	}

	logger := f.buildChannel(channelName)
	f.channels[channelName] = logger

	return logger
}

// buildChannel 按通道配置调用注册的驱动；通道不存在、驱动缺失或构建失败时
// 回退到 stderr/console/debug 并通过兜底 logger 发出告警（内容含缺失通道名），
// 保证应用照常启动。兜底构建直接走 newChannelLogger，不经过 Channel()，
// 因此不会递归。
// buildChannel invokes the registered driver with the channel config; on a
// missing channel, a missing driver or a build error it falls back to
// stderr/console/debug with a warning (containing the missing channel name)
// emitted through the fallback logger itself, so the application still starts.
// The fallback builds directly through newChannelLogger without going through
// Channel(), so it cannot recurse.
func (f *Factory) buildChannel(channelName string) contracts.ChannelLogger {
	channelConfig, ok := f.config.Channels[channelName]
	if !ok {
		return fallbackChannel(fmt.Errorf(
			"log: 日志通道 %q 未配置 log channel %q is not configured", channelName, channelName,
		))
	}

	driverName, _ := channelConfig[KeyDriver].(string)

	driver, ok := f.drivers[driverName]
	if !ok {
		return fallbackChannel(fmt.Errorf(
			"log: 通道 %q 的驱动 %q 未注册 log channel %q driver %q is not registered",
			channelName, driverName, channelName, driverName,
		))
	}

	logger, err := driver(channelName, channelConfig)
	if err != nil {
		return fallbackChannel(err)
	}

	// 通道名注入：zap 适配实现写入 encoder 的 channel 键，让多通道输出可区分来源。
	// Inject the channel name: zap-backed loggers write it to the encoder's
	// channel key so multi-channel output distinguishes its source.
	logger = withChannelName(logger, channelName)

	if channelLogger, ok := logger.(contracts.ChannelLogger); ok {
		return channelLogger
	}

	// 驱动只实现了 Logger 时，按级别分派包装为 ChannelLogger。
	// Wrap a plain Logger as ChannelLogger by dispatching on level.
	return &levelDispatchLogger{logger: logger}
}

// levelDispatchLogger 将仅实现 Logger 的驱动包装为支持按级别记录的 ChannelLogger。
// levelDispatchLogger wraps a driver that only implements Logger into a ChannelLogger.
type levelDispatchLogger struct {
	logger contracts.Logger
}

// Log 按给定级别分派到 Logger 的对应方法；未知级别降级为 Info，
// 与 zap.go 的 toZapLevel 保持一致的兜底语义。
// Log dispatches to the matching Logger method for the given level; unknown
// levels degrade to Info, consistent with toZapLevel in zap.go.
func (l *levelDispatchLogger) Log(level contracts.LogLevel, msg string) {
	switch level {
	case contracts.DebugLevel:
		l.logger.Debug(msg)
	case contracts.InfoLevel:
		l.logger.Info(msg)
	case contracts.WarnLevel:
		l.logger.Warn(msg)
	case contracts.ErrorLevel:
		l.logger.Error(msg)
	case contracts.FatalLevel:
		l.logger.Fatal(msg)
	default:
		l.logger.Info(msg)
	}
}

func (l *levelDispatchLogger) WithFields(fields contracts.Fields) contracts.Logger {
	return &levelDispatchLogger{logger: l.logger.WithFields(fields)}
}

func (l *levelDispatchLogger) WithField(key string, value any) contracts.Logger {
	return &levelDispatchLogger{logger: l.logger.WithField(key, value)}
}

func (l *levelDispatchLogger) WithError(err error) contracts.Logger {
	return &levelDispatchLogger{logger: l.logger.WithError(err)}
}

func (l *levelDispatchLogger) WithException(exception contracts.Exception) contracts.Logger {
	return &levelDispatchLogger{logger: l.logger.WithException(exception)}
}

func (l *levelDispatchLogger) Debug(msg string) { l.logger.Debug(msg) }
func (l *levelDispatchLogger) Info(msg string)  { l.logger.Info(msg) }
func (l *levelDispatchLogger) Warn(msg string)  { l.logger.Warn(msg) }
func (l *levelDispatchLogger) Error(msg string) { l.logger.Error(msg) }
func (l *levelDispatchLogger) Fatal(msg string) { l.logger.Fatal(msg) }

// fallbackChannel 构建兜底 logger（stderr/console/debug），并输出构建失败的原因。
// 保持 stderr 直连而不过 drivers 包（Task 6 收编兜底路径），确保兜底永不依赖驱动构建成功。
// fallbackChannel builds the fallback logger (stderr/console/debug) and reports
// why the original channel failed to build. It stays wired straight to stderr
// instead of going through the drivers subpackage (the fallback path is folded
// in by Task 6), guaranteeing the fallback never depends on a driver building.
func fallbackChannel(reason error) contracts.ChannelLogger {
	logger, err := newChannelLogger(os.Stderr, "debug", "console")
	if err != nil {
		// 兜底构建几乎不可能失败，失败时静默降级为空 logger。
		// The fallback build is nearly impossible to fail; degrade silently otherwise.
		return &zapLogger{l: zap.NewNop()}
	}

	logger.Warn("log 通道构建失败，已回退到 stderr/console/debug: " + reason.Error())

	return logger
}
