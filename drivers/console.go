// drivers：log 组件的正式内置驱动子包，只提供输出写入器（writer）。
// writer + encoder + level → logger 的组合仍由 log 包的 factory 完成：
// drivers 不能反向引用 log 包（依赖方向 drivers → contracts + zapcore，
// log → drivers），否则成环。
//
// drivers: the formal built-in driver subpackage of the log component,
// providing output writers only. Combining writer + encoder + level into a
// logger stays in the log package's factory: drivers must not import the log
// package back (dependency direction drivers -> contracts + zapcore,
// log -> drivers), which would create a cycle.
package drivers

import (
	"fmt"
	"os"
	"strings"

	"github.com/goal-web/contracts"
	"go.uber.org/zap/zapcore"
)

// keyOutput 是通道配置中输出通道（stdout/stderr）的键，与 log 包的
// log.KeyOutput（"output"）对应；drivers 是 log 的子包、不能反向引用，
// 故在此以私有常量复刻，避免跨包耦合。
// keyOutput is the channel key holding the output stream (stdout/stderr),
// mirroring log.KeyOutput ("output") in the log package; drivers is a child
// package that must not import log back, so the key is replicated here as a
// private constant to avoid cross-package coupling.
const keyOutput = "output"

// NewConsoleWriter 从通道配置构建 console 驱动的输出写入器：
//   - output：stdout（缺省）/stderr，不区分大小写；
//   - 字段存在但类型不是 string，或值为未知流名时返回 error。
//
// 返回 zapcore.AddSync 包装的标准流：os.File 的写入在进程级天然串行，
// 无需再加 zapcore.Lock。
//
// NewConsoleWriter builds the console driver's output writer from the channel
// config:
//   - output: stdout (default) / stderr, case-insensitive;
//   - a present non-string value or an unknown stream name returns an error.
//
// The returned standard streams are wrapped with zapcore.AddSync: writes to an
// os.File are process-level serial already, so no extra zapcore.Lock is needed.
func NewConsoleWriter(cfg contracts.Fields) (zapcore.WriteSyncer, error) {
	output, err := configString(cfg, keyOutput)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(output) {
	case "", "stdout":
		return zapcore.AddSync(os.Stdout), nil
	case "stderr":
		return zapcore.AddSync(os.Stderr), nil
	default:
		return nil, fmt.Errorf("log: 未知的输出通道 %q unknown output stream %q", output, output)
	}
}

// configString 读取字符串配置项：字段缺失或值为 nil 时返回空串（由调用方应用
// 缺省值或必填校验），字段存在但类型不是 string 时返回 error。与 log 包的同名
// 函数语义一致——复刻同样是出于不能反向引用 log 包的约束。
// configString reads a string config entry: a missing field or nil value yields
// "" (the caller applies its default or required check), while a present
// non-string value returns an error. Semantics match the log package's
// counterpart — replicated for the same no-back-import constraint.
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
