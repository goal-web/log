package tests

import (
	"testing"

	"github.com/goal-web/contracts"
	"github.com/goal-web/log"
	"github.com/stretchr/testify/assert"
)

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

// TestDefaultChannelMissing 验证 default 指向不存在的通道时返回兜底 logger，不 panic。
// TestDefaultChannelMissing verifies a default pointing to a nonexistent channel
// returns the fallback logger without panicking.
func TestDefaultChannelMissing(t *testing.T) {
	mgr := log.NewFactory(log.Config{Default: "nope"})

	assert.NotNil(t, mgr.Channel())
}
