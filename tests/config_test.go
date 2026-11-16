package tests

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/goal-web/contracts"
	"github.com/goal-web/log"
	"github.com/stretchr/testify/assert"
)

// captureStderr 临时把 os.Stderr 替换为管道写端并执行 fn，返回期间写入
// stderr 的全部内容；测试结束前恢复原值。兜底 Warn 走 os.Stderr，借此断言。
// captureStderr temporarily swaps os.Stderr for a pipe write end while fn runs
// and returns everything written to stderr; the original is restored before
// the test ends. The fallback Warn goes to os.Stderr, enabling this assertion.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe 失败 failed: %v", err)
	}

	original := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = original }()

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	fn()

	// 关闭写端使读端 ReadAll 返回。
	// Close the write end so ReadAll on the read end returns.
	w.Close()
	out := <-done
	r.Close()

	return out
}

// TestZeroConfigFallback 验证全空配置不 panic，工厂返回内置 console/debug 兜底 logger。
// TestZeroConfigFallback verifies an empty config does not panic: the factory
// returns the built-in console/debug fallback logger.
func TestZeroConfigFallback(t *testing.T) {
	mgr := log.NewFactory(log.Config{}) // 全空配置 fully empty config
	logger := mgr.Channel()

	assert.NotNil(t, logger)
	logger.Info("zero-config works") // 不 panic 即可（输出到 stderr，不断言内容）must not panic; output goes to stderr
}

// TestParseConfig 验证从原始 Fields 解析出 Default 与 Channels。
// TestParseConfig verifies Default and Channels are parsed from raw Fields.
func TestParseConfig(t *testing.T) {
	cfg := log.ParseConfig(contracts.Fields{
		log.KeyDefault: "file",
		log.KeyChannels: contracts.Fields{
			"file": contracts.Fields{log.KeyDriver: "file", log.KeyLevel: "info"},
		},
	})

	assert.Equal(t, "file", cfg.Default)
	assert.Len(t, cfg.Channels, 1)
	assert.Equal(t, "info", cfg.Channels["file"][log.KeyLevel])
}

// TestParseConfigInvalidRaw 验证原始配置不是 Fields（含 nil）时返回零配置，不 panic。
// TestParseConfigInvalidRaw verifies a non-Fields raw config (including nil)
// yields the zero Config without panicking.
func TestParseConfigInvalidRaw(t *testing.T) {
	assert.Equal(t, log.Config{}, log.ParseConfig("bad"))
	assert.Equal(t, log.Config{}, log.ParseConfig(nil))
	assert.Equal(t, log.Config{}, log.ParseConfig(123))
}

// TestParseConfigChannelsWrongType 验证 channels 键存在但类型错误时按缺失处理。
// TestParseConfigChannelsWrongType verifies a mistyped channels key is treated as missing.
func TestParseConfigChannelsWrongType(t *testing.T) {
	cfg := log.ParseConfig(contracts.Fields{log.KeyChannels: "oops"})

	assert.Empty(t, cfg.Channels)
}

// TestParseConfigNestedWrongType 验证 default 或单个通道条目类型错误时按缺失/跳过处理，不影响其余通道。
// TestParseConfigNestedWrongType verifies a mistyped default or a non-Fields
// channel entry is treated as missing/skipped without affecting other channels.
func TestParseConfigNestedWrongType(t *testing.T) {
	cfg := log.ParseConfig(contracts.Fields{
		log.KeyDefault: 123,
		log.KeyChannels: contracts.Fields{
			"bad":  "not-a-fields",
			"good": contracts.Fields{log.KeyDriver: "console"},
		},
	})

	assert.Empty(t, cfg.Default)
	assert.Len(t, cfg.Channels, 1)
	assert.Equal(t, "console", cfg.Channels["good"][log.KeyDriver])
}

// TestDefaultChannelMissing 验证 default 指向不存在的通道时返回兜底 logger，
// 不 panic，且经兜底 logger 向 stderr 打含缺失通道名的 Warn。
// TestDefaultChannelMissing verifies a default pointing to a nonexistent
// channel returns the fallback logger without panicking, and a Warn carrying
// the missing channel name is emitted through the fallback logger to stderr.
func TestDefaultChannelMissing(t *testing.T) {
	mgr := log.NewFactory(log.Config{Default: "nope"})

	var logger contracts.ChannelLogger
	out := captureStderr(t, func() {
		logger = mgr.Channel()
	})

	assert.NotNil(t, logger)
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "nope")
}

// TestUnregisteredDriverFallsBack 验证通道驱动未注册时返回兜底 logger 并打 Warn。
// TestUnregisteredDriverFallsBack verifies an unregistered channel driver
// returns the fallback logger with a Warn.
func TestUnregisteredDriverFallsBack(t *testing.T) {
	mgr := log.NewFactory(log.Config{
		Default: "app",
		Channels: map[string]contracts.Fields{
			"app": {log.KeyDriver: "nope"},
		},
	})

	var logger contracts.ChannelLogger
	out := captureStderr(t, func() {
		logger = mgr.Channel()
	})

	assert.NotNil(t, logger)
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "nope")
}

// TestDriverBuildErrorFallsBack 验证驱动构建失败时返回兜底 logger 并打含错误原因的 Warn。
// TestDriverBuildErrorFallsBack verifies a failing driver build returns the
// fallback logger with a Warn carrying the build error.
func TestDriverBuildErrorFallsBack(t *testing.T) {
	mgr := log.NewFactory(log.Config{
		Default: "broken",
		Channels: map[string]contracts.Fields{
			"broken": {log.KeyDriver: "broken"},
		},
	})
	mgr.Extend("broken", func(string, contracts.Fields) (contracts.Logger, error) {
		return nil, errors.New("boom")
	})

	var logger contracts.ChannelLogger
	out := captureStderr(t, func() {
		logger = mgr.Channel()
	})

	assert.NotNil(t, logger)
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "boom")
}

// TestParseConfigTypedNilChannelEntries 锁定 channels 条目两种 nil 的当前走向：
// typed nil（类型仍为 contracts.Fields）被保留为 nil map 条目；
// 未类型化 nil（接口值为 nil）类型断言失败，被跳过。
// TestParseConfigTypedNilChannelEntries locks the current behavior for the two
// nil shapes of a channels entry: a typed nil (dynamic type contracts.Fields)
// is kept as a nil map entry, while an untyped nil (nil interface value) fails
// the type assertion and is skipped.
func TestParseConfigTypedNilChannelEntries(t *testing.T) {
	typed := log.ParseConfig(contracts.Fields{
		log.KeyChannels: contracts.Fields{"a": contracts.Fields(nil)},
	})
	assert.Contains(t, typed.Channels, "a")
	assert.Nil(t, typed.Channels["a"])

	untyped := log.ParseConfig(contracts.Fields{
		log.KeyChannels: contracts.Fields{"a": nil},
	})
	assert.NotContains(t, untyped.Channels, "a")
}
