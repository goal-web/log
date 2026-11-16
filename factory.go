package log

import (
	"fmt"
	"os"
	"sync"

	"github.com/goal-web/contracts"
	"go.uber.org/zap"
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

// Factory 是 contracts.LogManager 的最小实现：按名称懒构建通道并缓存，
// 支持运行时注册自定义驱动。Task 4/6 将重写为完整版本。
// Factory is a minimal contracts.LogManager: channels are built lazily by name
// and cached, with runtime custom driver registration. Task 4/6 will replace
// this with the full implementation.
type Factory struct {
	config   Config
	drivers  map[string]contracts.LogDriver
	channels map[string]contracts.ChannelLogger
	mutex    sync.Mutex
}

// NewFactory 用给定配置创建 LogManager。
// NewFactory creates a LogManager with the given configuration.
func NewFactory(config Config) contracts.LogManager {
	return &Factory{
		config:   config,
		drivers:  map[string]contracts.LogDriver{},
		channels: map[string]contracts.ChannelLogger{},
	}
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

// buildChannel 按通道配置调用注册的驱动；驱动缺失或构建失败时回退到
// stderr/console/debug 并发出告警，保证应用照常启动。
// buildChannel invokes the registered driver with the channel config; on a
// missing driver or a build error it falls back to stderr/console/debug with a
// warning so the application still starts.
func (f *Factory) buildChannel(channelName string) contracts.ChannelLogger {
	channelConfig := f.config.Channels[channelName]
	driverName, _ := channelConfig["driver"].(string)

	driver, ok := f.drivers[driverName]
	if ok {
		logger, err := driver(channelName, channelConfig)
		if err == nil {
			if channelLogger, ok := logger.(contracts.ChannelLogger); ok {
				return channelLogger
			}

			// 驱动只实现了 Logger 时，按级别分派包装为 ChannelLogger。
			// Wrap a plain Logger as ChannelLogger by dispatching on level.
			return &levelDispatchLogger{logger: logger}
		}

		return fallbackChannel(err)
	}

	return fallbackChannel(fmt.Errorf("log: 通道 %q 的驱动 %q 未注册 channel driver not registered", channelName, driverName))
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
// fallbackChannel builds the fallback logger (stderr/console/debug) and reports
// why the original channel failed to build.
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
