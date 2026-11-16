package tests

import (
	"bytes"
	"strings"
	"testing"

	"github.com/goal-web/contracts"
	"github.com/goal-web/log"
	"github.com/stretchr/testify/assert"
)

// fakeException 是测试用 contracts.Exception 假实现，支持挂载上一级异常。
// fakeException is a test fake for contracts.Exception with a previous chain.
type fakeException struct {
	message  string
	previous contracts.Exception
}

func (e *fakeException) Error() string                    { return e.message }
func (e *fakeException) GetPrevious() contracts.Exception { return e.previous }

// newBufferChannel 通过自定义 buffer 驱动构建一个 debug 级别的默认通道。
// newBufferChannel builds a debug-level default channel writing into an in-memory buffer via a custom driver.
func newBufferChannel(t *testing.T, format string) (*bytes.Buffer, contracts.ChannelLogger) {
	return newBufferChannelWithLevel(t, "debug", format)
}

// newBufferChannelWithLevel 通过自定义 buffer 驱动构建指定级别的默认通道。
// newBufferChannelWithLevel builds a default channel at the given level via a custom driver.
func newBufferChannelWithLevel(t *testing.T, level, format string) (*bytes.Buffer, contracts.ChannelLogger) {
	t.Helper()

	buf := &bytes.Buffer{}
	mgr := log.NewFactory(log.Config{
		Default: "test",
		Channels: map[string]contracts.Fields{
			"test": {"driver": "buffer", "level": level, "format": format, "writer": buf},
		},
	})
	mgr.Extend("buffer", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		return log.NewBufferLogger(cfg)
	})

	return buf, mgr.Channel()
}

// TestLevelMethods 验证四级方法按对应级别输出，且未触发的 FATAL 不应出现。
// TestLevelMethods verifies the four level methods emit at their levels and no FATAL appears.
func TestLevelMethods(t *testing.T) {
	buf, logger := newBufferChannel(t, "console")

	logger.Debug("d")
	logger.Info("i")
	logger.Warn("w")
	logger.Error("e")

	out := buf.String()
	for _, fragment := range []string{"d", "i", "w", "e", "DEBUG", "INFO", "WARN", "ERROR"} {
		assert.Contains(t, out, fragment)
	}
	assert.NotContains(t, out, "FATAL")
}

// TestWithChain 验证 With 链字段随日志输出，且链式调用不影响原实例（不可变语义）。
// TestWithChain verifies With-chain fields are logged and the original instance stays untouched (immutability).
func TestWithChain(t *testing.T) {
	buf, logger := newBufferChannel(t, "json")

	chained := logger.
		WithField("k1", "v1").
		WithFields(map[string]any{"k2": 2}).
		WithError(assert.AnError)
	chained.Info("chained")
	logger.Info("bare") // 原实例不应携带链上的字段 the original must not carry chained fields

	out := buf.String()
	assert.Contains(t, out, `"k1":"v1"`)
	assert.Contains(t, out, `"k2":2`)
	assert.Contains(t, out, "chained")
	assert.Contains(t, out, "assert.AnError")
	assert.Contains(t, out, "bare")
	assert.Equal(t, 1, strings.Count(out, `"k1":"v1"`), "With 链不应污染原实例 With-chain must not leak into the original logger")
}

// TestLogByLevel 验证 Log 按给定级别分派输出。
// TestLogByLevel verifies Log dispatches at the given level.
func TestLogByLevel(t *testing.T) {
	buf, logger := newBufferChannel(t, "console")

	logger.Log(contracts.WarnLevel, "by-level")

	out := buf.String()
	assert.Contains(t, out, "by-level")
	assert.Contains(t, out, "WARN")
}

// TestLevelFilter 验证低于通道级别的日志在格式化前被过滤。
// TestLevelFilter verifies records below the channel level are filtered before formatting.
func TestLevelFilter(t *testing.T) {
	buf, logger := newBufferChannelWithLevel(t, "warn", "console")

	logger.Info("dropped")
	logger.Warn("kept")

	out := buf.String()
	assert.Contains(t, out, "kept")
	assert.NotContains(t, out, "dropped")
}

// TestWithException 验证异常消息与 previous 链信息随日志输出。
// TestWithException verifies the exception message and its previous chain are logged.
func TestWithException(t *testing.T) {
	buf, logger := newBufferChannel(t, "json")

	exc := &fakeException{
		message:  "top-level failure",
		previous: &fakeException{message: "root cause"},
	}
	logger.WithException(exc).Error("with-exc")

	out := buf.String()
	assert.Contains(t, out, "with-exc")
	assert.Contains(t, out, `"error":"top-level failure"`)
	assert.Contains(t, out, `"previous":"root cause"`)
}
