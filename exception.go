// exception.go：日志组件错误定义的收口文件——组件内的命名哨兵错误统一定义
// 在此；其余构建失败（通道缺失、驱动未注册、级别/格式非法、stack 配置错误等）
// 经 fmt.Errorf 携带上下文就地构造，最终统一交给 fallbackChannel 兜底 + Warn，
// 组件按设计从不 panic。
//
// 刻意不引入 cache 惯例的 DriverException = exceptions.Exception 别名：
// 设计上 log 不 import supports（依赖方向 supports/logs → log → contracts），
// 且组件从不 panic、无异常类型构造点，引入只会是一处死代码。
//
// exception.go: the error-definition home of the log component — the
// component's named sentinel errors live here; other build failures (missing
// channels, unregistered drivers, invalid levels/formats, bad stack configs,
// etc.) are constructed on the spot with fmt.Errorf carrying context and all
// end up in fallbackChannel's fallback + Warn — the component never panics by
// design.
//
// The cache-convention DriverException = exceptions.Exception alias is
// deliberately omitted: by design log does not import supports (dependency
// direction supports/logs -> log -> contracts), and since the component never
// panics there is no construction site for such an exception type — it would
// be dead code.
package log

import (
	"errors"
)

// errDefaultChannelMissing 描述 channels 有但 default 缺失的兜底原因
// （首次触发时 Warn 一次，结果进缓存不重复刷屏）。
// errDefaultChannelMissing describes the fallback reason when channels exist
// but the default is missing (warned once on first use, then cached).
var errDefaultChannelMissing = errors.New(
	"log: default 未配置，已使用兜底通道 log default channel is not configured, using the fallback channel",
)
