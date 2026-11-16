// zap 适配层：将 contracts.Logger/ChannelLogger 映射到 zap，并
// 提供从通道配置（Fields）构建 logger 的标准入口。
//
// The zap adapter: maps contracts.Logger/ChannelLogger onto zap and provides
// the standard entry point for building loggers from channel configs (Fields).
package log

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/goal-web/contracts"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// timeLayout 是 console/json 两种格式共用的时间输出布局。
// timeLayout is the time layout shared by the console and json formats.
const timeLayout = "2006-01-02 15:04:05"

// zapLogger 是 contracts.Logger/ChannelLogger 的 zap 适配实现。
// zapLogger is the zap-backed implementation of contracts.Logger/ChannelLogger.
type zapLogger struct {
	l *zap.Logger
}

// Debug 在 DEBUG 级别记录日志。
// Debug logs a record at the DEBUG level.
func (z *zapLogger) Debug(msg string) { z.l.Debug(msg) }

// Info 在 INFO 级别记录日志。
// Info logs a record at the INFO level.
func (z *zapLogger) Info(msg string) { z.l.Info(msg) }

// Warn 在 WARNING 级别记录日志。
// Warn logs a record at the WARNING level.
func (z *zapLogger) Warn(msg string) { z.l.Warn(msg) }

// Error 在 ERROR 级别记录日志。
// Error logs a record at the ERROR level.
func (z *zapLogger) Error(msg string) { z.l.Error(msg) }

// Fatal 在 FATAL 级别记录日志并退出进程（zap 语义：写完日志后 os.Exit(1)）。
// Fatal logs a record at the FATAL level and exits the process (zap semantics:
// os.Exit(1) after the record is written).
func (z *zapLogger) Fatal(msg string) { z.l.Fatal(msg) }

// Log 按给定级别分派到对应的 zap 记录方法。
// Log dispatches to the matching zap method for the given level.
func (z *zapLogger) Log(level contracts.LogLevel, msg string) {
	z.l.Log(toZapLevel(level), msg)
}

// WithFields 添加一组字段，返回携带新字段的独立实例（zap.Logger.With 不可变语义）。
// WithFields adds fields and returns an independent instance carrying them
// (zap.Logger.With immutability semantics).
func (z *zapLogger) WithFields(fields contracts.Fields) contracts.Logger {
	next := z.l
	for key, value := range fields {
		next = next.With(zap.Any(key, value))
	}

	return &zapLogger{l: next}
}

// WithField 通过给定的 key value 添加数据。
// WithField adds data by the given key and value.
func (z *zapLogger) WithField(key string, value any) contracts.Logger {
	return &zapLogger{l: z.l.With(zap.Any(key, value))}
}

// WithError 添加错误（字段名为 "error"）。
// WithError adds an error under the "error" field key.
func (z *zapLogger) WithError(err error) contracts.Logger {
	return &zapLogger{l: z.l.With(zap.Error(err))}
}

// WithException 记录异常及其 previous 链（对齐旧 apex 适配的 error + previous 行为）。
// WithException records the exception and its previous chain
// (aligned with the legacy apex adapter's error + previous behavior).
func (z *zapLogger) WithException(exception contracts.Exception) contracts.Logger {
	if exception == nil {
		return z
	}

	return &zapLogger{l: z.l.With(
		zap.Error(exception),
		zap.Any("previous", exception.GetPrevious()),
	)}
}

// toZapLevel 将 contracts.LogLevel 映射为 zapcore.Level；未知级别降级为 Info，
// 避免 zap.Logger.Log 对非法级别 panic。
// toZapLevel maps contracts.LogLevel to zapcore.Level; unknown levels degrade to
// Info so zap.Logger.Log never panics on an invalid level.
func toZapLevel(level contracts.LogLevel) zapcore.Level {
	switch level {
	case contracts.DebugLevel:
		return zapcore.DebugLevel
	case contracts.InfoLevel:
		return zapcore.InfoLevel
	case contracts.WarnLevel:
		return zapcore.WarnLevel
	case contracts.ErrorLevel:
		return zapcore.ErrorLevel
	case contracts.FatalLevel:
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

// withChannelName 为 zap 适配实现注入通道名（写入 encoder 的 channel 键），
// 非 zap 实现原样返回。工厂构建通道后调用，使多通道输出可区分来源。
// withChannelName attaches the channel name to a zap-backed logger (written to
// the encoder's channel key) and returns other Logger implementations
// unchanged. The factory calls it after building a channel so multi-channel
// output distinguishes its source.
func withChannelName(logger contracts.Logger, channelName string) contracts.Logger {
	zl, ok := logger.(*zapLogger)
	if !ok {
		return logger
	}

	return &zapLogger{l: zl.l.Named(channelName)}
}

// NewBufferLogger 从通道配置构建写入任意 io.Writer 的 logger，
// 是自定义驱动与测试的标准入口。cfg 支持的字段：
//
//   - writer (io.Writer)：必填，日志输出目标；非 WriteSyncer（如 bytes.Buffer）
//     会自动加锁，保证可被多个 goroutine 安全共享
//   - level (string)：debug/info/warn/warning/error/fatal，不区分大小写，
//     缺省 debug；字段缺失或值为 nil 走默认，存在但类型错误或值非法时返回 error
//   - format (string)：console/json，不区分大小写，缺省 console；错误策略同 level
//
// NewBufferLogger builds a logger writing to any io.Writer from channel
// configuration; it is the standard entry point for custom drivers and tests.
func NewBufferLogger(cfg contracts.Fields) (contracts.Logger, error) {
	writerValue, ok := cfg[KeyWriter]
	if !ok || writerValue == nil {
		return nil, fmt.Errorf("log: buffer 驱动配置缺少 writer 字段 buffer driver config requires a writer field")
	}

	writer, ok := writerValue.(io.Writer)
	if !ok {
		return nil, fmt.Errorf("log: writer 字段需实现 io.Writer，实际类型 %T writer field must implement io.Writer, got %T", writerValue, writerValue)
	}

	level, err := configString(cfg, KeyLevel)
	if err != nil {
		return nil, err
	}

	format, err := configString(cfg, KeyFormat)
	if err != nil {
		return nil, err
	}

	// bytes.Buffer 等普通 io.Writer 自身不并发安全，需加锁后才能被多 goroutine 共享；
	// os.File 等原生 WriteSyncer 原样透传，零额外开销。
	// Plain io.Writer values (e.g. bytes.Buffer) are not concurrency-safe, so they
	// are wrapped in a lock before being shared across goroutines; native
	// WriteSyncer values such as os.File pass through with no extra overhead.
	var writeSyncer zapcore.WriteSyncer
	if ws, ok := writer.(zapcore.WriteSyncer); ok {
		writeSyncer = ws
	} else {
		writeSyncer = zapcore.Lock(zapcore.AddSync(writer))
	}

	return newChannelLogger(writeSyncer, level, format)
}

// configString 读取字符串配置项：字段缺失或值为 nil 时返回空串（由调用方应用默认值），
// 字段存在但类型不是 string 时返回 error（与非法字符串值的严格策略保持一致）。
// configString reads a string config entry: a missing field or nil value yields
// "" (the caller applies its default), while a present non-string value returns
// an error, consistent with the strict policy for invalid string values.
func configString(cfg contracts.Fields, key string) (string, error) {
	value, ok := cfg[key]
	if !ok || value == nil {
		return "", nil
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("log: %s 字段需为字符串，实际类型 %T %s field must be a string, got %T", key, value, key, value)
	}

	return str, nil
}

// newChannelLogger 用给定的写入器、级别与格式组装 zap 通道 logger，
// 供各内置驱动复用。级别或格式非法时返回 error 而非 panic。
// newChannelLogger assembles a zap channel logger from the given writer,
// level and format, for reuse by the built-in drivers. It returns an error
// instead of panicking on an invalid level or format.
func newChannelLogger(writer zapcore.WriteSyncer, levelStr, format string) (*zapLogger, error) {
	encoder, err := newEncoder(format)
	if err != nil {
		return nil, err
	}

	level, err := parseLevel(levelStr)
	if err != nil {
		return nil, err
	}

	core := zapcore.NewCore(encoder, writer, level)

	return &zapLogger{l: zap.New(core)}, nil
}

// newEncoder 按格式构建 zapcore.Encoder（不区分大小写）：json 或 console（缺省）。
// newEncoder builds a zapcore.Encoder for the format (case-insensitive):
// json or console (default).
func newEncoder(format string) (zapcore.Encoder, error) {
	switch strings.ToLower(format) {
	case "json":
		return zapcore.NewJSONEncoder(newEncoderConfig()), nil
	case "", "console":
		return zapcore.NewConsoleEncoder(newEncoderConfig()), nil
	default:
		return nil, fmt.Errorf("log: 未知的日志格式 %q unknown log format", format)
	}
}

// parseLevel 解析级别字符串（不区分大小写，"warning" 作为 "warn" 的别名）；
// 空值缺省为 debug，非法值返回 error。
// parseLevel parses a level string (case-insensitive, "warning" accepted as an
// alias of "warn"); empty falls back to debug, invalid values return an error.
func parseLevel(levelStr string) (zapcore.Level, error) {
	switch strings.ToLower(levelStr) {
	case "", "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "warn", "warning":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	case "fatal":
		return zapcore.FatalLevel, nil
	default:
		return zapcore.DebugLevel, fmt.Errorf("log: 未知的日志级别 %q unknown log level", levelStr)
	}
}

// newEncoderConfig 返回 console/json 共用的编码配置：
// 时间输出为 "2006-01-02 15:04:05"，级别大写，通道名键为 channel，不输出 caller。
// newEncoderConfig returns the encoder config shared by console/json: time as
// "2006-01-02 15:04:05", capital levels, channel name key, no caller output.
func newEncoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "channel",
		CallerKey:      "",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     customTimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
}

// customTimeEncoder 按固定布局输出时间。
// customTimeEncoder encodes the time with the fixed layout.
func customTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format(timeLayout))
}
