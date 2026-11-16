package tests

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goal-web/contracts"
	"github.com/goal-web/log"
	"github.com/stretchr/testify/assert"
)

// newBufferStackFactory 构建以 a/b 两个 buffer 通道为子通道、默认通道名为
// defaultName 的工厂；buffer 驱动经 Extend 注册，两个子通道均为 debug/console。
// newBufferStackFactory builds a factory whose default channel defaultName is a
// stack over two debug/console buffer child channels a and b; the buffer
// driver is registered via Extend.
func newBufferStackFactory(defaultName string, stackLevel string) (*bytes.Buffer, *bytes.Buffer, contracts.LogManager) {
	buf1, buf2 := &bytes.Buffer{}, &bytes.Buffer{}
	mgr := log.NewFactory(log.Config{
		Default: defaultName,
		Channels: map[string]contracts.Fields{
			"a": {log.KeyDriver: "buffer", log.KeyWriter: buf1, log.KeyLevel: "debug", log.KeyFormat: "console"},
			"b": {log.KeyDriver: "buffer", log.KeyWriter: buf2, log.KeyLevel: "debug", log.KeyFormat: "console"},
			"stack": {
				log.KeyDriver: "stack",
				log.KeyLevel:  stackLevel,
				"channels":    []string{"a", "b"},
			},
		},
	})
	mgr.Extend("buffer", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		return log.NewBufferLogger(cfg)
	})

	return buf1, buf2, mgr
}

// TestChannelCache 锁定通道缓存语义：同一通道的两次 Channel() 返回同一实例
// （默认通道与显式命名通道均如此），构建至多一次。
// TestChannelCache locks the channel cache semantics: two Channel() calls for
// the same channel return the same instance (both the default and an
// explicitly named channel), with at most one build.
func TestChannelCache(t *testing.T) {
	var builds int32
	buf := &bytes.Buffer{}
	mgr := log.NewFactory(log.Config{
		Default: "app",
		Channels: map[string]contracts.Fields{
			"app": {log.KeyDriver: "buffer", log.KeyWriter: buf, log.KeyLevel: "debug", log.KeyFormat: "console"},
		},
	})
	mgr.Extend("buffer", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		atomic.AddInt32(&builds, 1)
		return log.NewBufferLogger(cfg)
	})

	first := mgr.Channel()
	assert.Same(t, first, mgr.Channel(), "默认通道两次调用应返回同一实例 default channel must be cached")
	assert.Same(t, first, mgr.Channel("app"), "显式命名通道应命中缓存 named channel must hit the cache")
	assert.Equal(t, int32(1), atomic.LoadInt32(&builds), "构建至多一次 must build at most once")
}

// TestStackFanout 验证 stack 组合通道经 zapcore.NewTee 扇出：一条日志同时
// 写入所有子通道，且 Named 注入在 stack 上同样生效（两条输出都带通道名）。
// TestStackFanout verifies stack fan-out via zapcore.NewTee: one record reaches
// every child channel, and Named injection works on the stack itself (both
// outputs carry the channel name).
func TestStackFanout(t *testing.T) {
	buf1, buf2, mgr := newBufferStackFactory("stack", "debug")

	logger := mgr.Channel()
	assert.Same(t, logger, mgr.Channel("stack"), "stack 通道同样进入缓存 stack channel must be cached too")

	logger.Info("fan")

	for name, buf := range map[string]*bytes.Buffer{"a": buf1, "b": buf2} {
		out := buf.String()
		assert.Contains(t, out, "fan", "子通道 %s 应收到日志 child %s must receive the record", name, name)
		assert.Contains(t, out, "\tstack\t", "子通道 %s 的输出应带 stack 通道名 child %s output must carry the stack channel name", name, name)
	}
}

// TestStackLevelFilter 验证 stack 自身级别的过滤位置在 tee 之后：子通道为
// debug，但 stack 级别为 warn 时 Info 在进入子通道前被滤掉，Warn 通过。
// TestStackLevelFilter verifies the stack's own level filter sits after the
// tee: children are debug, but with the stack at warn an Info is dropped
// before reaching any child while a Warn passes through.
func TestStackLevelFilter(t *testing.T) {
	buf1, buf2, mgr := newBufferStackFactory("stack", "warn")

	logger := mgr.Channel()
	logger.Info("dropped")
	logger.Warn("passed")

	for name, buf := range map[string]*bytes.Buffer{"a": buf1, "b": buf2} {
		out := buf.String()
		assert.Contains(t, out, "passed", "子通道 %s 应收到 Warn child %s must receive the Warn", name, name)
		assert.NotContains(t, out, "dropped", "子通道 %s 不得收到被 stack 级别滤掉的 Info child %s must not receive the Info dropped by the stack level", name, name)
	}
}

// TestStackNested 验证 stack 嵌套 stack（stack 引用另一个 stack 通道）：
// 递归构建成功，三层扇出（top → mid → a/b）全部到达。
// TestStackNested verifies a stack referencing another stack channel: the
// recursive build succeeds and the three-level fan-out (top -> mid -> a/b)
// reaches every leaf.
func TestStackNested(t *testing.T) {
	buf1, buf2 := &bytes.Buffer{}, &bytes.Buffer{}
	mgr := log.NewFactory(log.Config{
		Default: "top",
		Channels: map[string]contracts.Fields{
			"a": {log.KeyDriver: "buffer", log.KeyWriter: buf1, log.KeyLevel: "debug", log.KeyFormat: "console"},
			"b": {log.KeyDriver: "buffer", log.KeyWriter: buf2, log.KeyLevel: "debug", log.KeyFormat: "console"},
			"mid": {
				log.KeyDriver: "stack",
				log.KeyLevel:  "debug",
				"channels":    []string{"a", "b"},
			},
			"top": {
				log.KeyDriver: "stack",
				log.KeyLevel:  "debug",
				"channels":    []string{"mid"},
			},
		},
	})
	mgr.Extend("buffer", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		return log.NewBufferLogger(cfg)
	})

	mgr.Channel().Info("deep")

	assert.Contains(t, buf1.String(), "deep", "叶子通道 a 应收到日志 leaf a must receive the record")
	assert.Contains(t, buf2.String(), "deep", "叶子通道 b 应收到日志 leaf b must receive the record")
}

// TestStackCycleProtection 验证 stack 引用自身时不死锁不栈溢出：返回兜底
// logger 并经 stderr 发出含通道名的循环引用 Warn，兜底 logger 仍可用。
// TestStackCycleProtection verifies a stack referencing itself neither
// deadlocks nor overflows: a fallback logger is returned with a circular
// reference Warn carrying the channel name on stderr, and the fallback stays
// usable.
func TestStackCycleProtection(t *testing.T) {
	mgr := log.NewFactory(log.Config{
		Default: "s",
		Channels: map[string]contracts.Fields{
			"s": {log.KeyDriver: "stack", log.KeyLevel: "debug", "channels": []string{"s"}},
		},
	})

	var logger contracts.ChannelLogger
	out := captureStderr(t, func() {
		logger = mgr.Channel()
		logger.Info("after-cycle")
	})

	assert.NotNil(t, logger)
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "循环引用", "应提示循环引用 must mention the circular reference")
	assert.Contains(t, out, "after-cycle", "兜底 logger 应继续可用 the fallback must stay usable")
}

// TestStackIndirectCycle 验证间接环 a→b→a 同样被环检测拦下：不死锁不栈溢出，
// 构建结果可用且 stderr 出现循环引用 Warn。
// TestStackIndirectCycle verifies the indirect cycle a->b->a is also caught by
// the cycle guard: no deadlock, no stack overflow, a usable result and a
// circular-reference Warn on stderr.
func TestStackIndirectCycle(t *testing.T) {
	mgr := log.NewFactory(log.Config{
		Default: "a",
		Channels: map[string]contracts.Fields{
			"a": {log.KeyDriver: "stack", log.KeyLevel: "debug", "channels": []string{"b"}},
			"b": {log.KeyDriver: "stack", log.KeyLevel: "debug", "channels": []string{"a"}},
		},
	})

	var logger contracts.ChannelLogger
	out := captureStderr(t, func() {
		logger = mgr.Channel()
		logger.Info("after-indirect-cycle")
	})

	assert.NotNil(t, logger)
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "循环引用", "应提示循环引用 must mention the circular reference")
	assert.Contains(t, out, "after-indirect-cycle", "兜底 logger 应继续可用 the fallback must stay usable")
}

// TestStackConfigErrorsFallsBack 验证 stack 的 channels 字段类型错误、缺失、
// 空数组或子元素非字符串时按配置错误处理：兜底 + Warn，不 panic。
// TestStackConfigErrorsFallsBack verifies mistyped, missing, empty or
// non-string-element channels fields are treated as configuration errors:
// fallback + Warn without panicking.
func TestStackConfigErrorsFallsBack(t *testing.T) {
	cases := []struct {
		name     string
		channels any
	}{
		{"channels wrong type", "not-an-array"},
		{"channels missing", nil},
		{"channels empty", []string{}},
		{"channels non-string element", []any{"a", 123}},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			cfg := contracts.Fields{log.KeyDriver: "stack", log.KeyLevel: "debug"}
			if tt.channels != nil {
				cfg["channels"] = tt.channels
			}

			mgr := log.NewFactory(log.Config{
				Default: "stack",
				Channels: map[string]contracts.Fields{
					"stack": cfg,
				},
			})

			var logger contracts.ChannelLogger
			// 兜底 logger 在构建时捕获 stderr，故两次动作放在同一捕获窗口内。
			// The fallback logger captures stderr at build time, so both actions
			// share one capture window.
			out := captureStderr(t, func() {
				logger = mgr.Channel()
				logger.Info("still-works")
			})

			assert.NotNil(t, logger)
			assert.Contains(t, out, "WARN")
			assert.Contains(t, out, "channels")
			assert.Contains(t, out, "still-works", "兜底 logger 应可用 the fallback must stay usable")
		})
	}
}

// TestStackZapChildOnly 验证仅实现 Logger 的自定义驱动作为 stack 子通道时
// 无法被 NewTee 组合：按配置错误处理，兜底 + Warn（zap 体系子通道才可 tee）。
// TestStackZapChildOnly verifies a custom driver returning a plain Logger
// cannot be tee'd as a stack child: it is handled as a configuration error
// with fallback + Warn (only zap-backed children compose).
func TestStackZapChildOnly(t *testing.T) {
	mgr := log.NewFactory(log.Config{
		Default: "stack",
		Channels: map[string]contracts.Fields{
			"plain": {log.KeyDriver: "plain", log.KeyLevel: "debug"},
			"stack": {log.KeyDriver: "stack", log.KeyLevel: "debug", "channels": []string{"plain"}},
		},
	})
	mgr.Extend("plain", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		return &fakePlainLogger{}, nil
	})

	var logger contracts.ChannelLogger
	out := captureStderr(t, func() {
		logger = mgr.Channel()
	})

	assert.NotNil(t, logger)
	assert.Contains(t, out, "WARN")
}

// TestCustomDriverTypeGuard 验证类型守卫：Extend 一个只实现 contracts.Logger
// 的驱动后，Channel() 返回值仍满足 contracts.ChannelLogger，Log 按级别正确
// 分派到驱动的对应方法。
// TestCustomDriverTypeGuard verifies the type guard: after extending with a
// driver that only implements contracts.Logger, Channel() still satisfies
// contracts.ChannelLogger and Log dispatches to the matching driver method.
func TestCustomDriverTypeGuard(t *testing.T) {
	fake := &fakePlainLogger{}
	mgr := log.NewFactory(log.Config{
		Default: "app",
		Channels: map[string]contracts.Fields{
			"app": {log.KeyDriver: "plain", log.KeyLevel: "debug"},
		},
	})
	mgr.Extend("plain", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		return fake, nil
	})

	logger := mgr.Channel()
	logger.Debug("d")
	logger.Info("i")
	logger.Warn("w")
	logger.Error("e")
	logger.Log(contracts.WarnLevel, "by-warn")
	logger.Log(contracts.InfoLevel, "by-info")

	assert.Equal(t, map[string]int{
		"Debug": 1,
		"Info":  2, // Log(InfoLevel) 降级分派 Log dispatches InfoLevel
		"Warn":  2,
		"Error": 1,
		"Fatal": 0,
	}, fake.snapshot(), "级别分派必须正确 levels must dispatch correctly")
}

// TestConcurrentFirstBuild 验证锁范围重构后的首次并发构建：多个 goroutine
// 同时首次请求同一通道时只构建一次（singleflight），且全部拿到同一实例。
// TestConcurrentFirstBuild verifies first concurrent builds after the lock
// scope rework: many goroutines racing on the same channel build it exactly
// once (singleflight) and all receive the same instance.
func TestConcurrentFirstBuild(t *testing.T) {
	var builds int32
	buf := &bytes.Buffer{}
	mgr := log.NewFactory(log.Config{
		Default: "app",
		Channels: map[string]contracts.Fields{
			"app": {log.KeyDriver: "buffer", log.KeyWriter: buf, log.KeyLevel: "debug", log.KeyFormat: "console"},
		},
	})
	mgr.Extend("buffer", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		atomic.AddInt32(&builds, 1)
		return log.NewBufferLogger(cfg)
	})

	const callers = 50
	instances := make([]contracts.ChannelLogger, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			instances[seq] = mgr.Channel()
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&builds), "并发首次构建至多构建一次 must build at most once")
	for seq := 1; seq < callers; seq++ {
		assert.Same(t, instances[0], instances[seq], "并发调用应共享同一实例 concurrent callers must share one instance")
	}
}

// TestDefaultMissingWarnOnce 验证 channels 有但 default 缺失的场景：首次
// Channel() 时 Warn 一次（内容说明 default 未配置并已使用兜底通道），缓存后
// 不重复刷屏。
// TestDefaultMissingWarnOnce verifies channels present but default missing:
// the first Channel() warns once explaining the default is unconfigured and
// the fallback is used, and the cached result never warns again.
func TestDefaultMissingWarnOnce(t *testing.T) {
	buf := &bytes.Buffer{}
	mgr := log.NewFactory(log.Config{
		Channels: map[string]contracts.Fields{
			"file": {log.KeyDriver: "buffer", log.KeyWriter: buf, log.KeyLevel: "debug", log.KeyFormat: "console"},
		},
	})

	var logger contracts.ChannelLogger
	var again contracts.ChannelLogger
	// 同一捕获窗口内调用两次：第二次命中缓存，提示恰好出现一次。
	// Two calls inside one capture window: the second hits the cache and the
	// warning appears exactly once.
	out := captureStderr(t, func() {
		logger = mgr.Channel()
		again = mgr.Channel()
	})

	assert.NotNil(t, logger)
	assert.Same(t, logger, again, "第二次调用应命中缓存 the second call must hit the cache")
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "default 未配置", "应说明 default 未配置 must explain the default is unconfigured")
	assert.Contains(t, out, "兜底", "应说明已使用兜底通道 must mention the fallback channel")
	assert.Equal(t, 1, strings.Count(out, "default 未配置"), "提示已缓存，不得重复 the warning must be cached")
}

// TestStackMissingChildFallsBack 验证 stack 引用未配置的子通道：子通道兜底
// （Warn 含子通道名），stack 仍构建成功并经兜底子通道向 stderr 输出。
// TestStackMissingChildFallsBack verifies a stack referencing an unconfigured
// child: the child falls back (Warn carries the child name), the stack still
// builds and reaches stderr through the fallback child.
func TestStackMissingChildFallsBack(t *testing.T) {
	mgr := log.NewFactory(log.Config{
		Default: "stack",
		Channels: map[string]contracts.Fields{
			"stack": {log.KeyDriver: "stack", log.KeyLevel: "debug", "channels": []string{"ghost"}},
		},
	})

	var logger contracts.ChannelLogger
	out := captureStderr(t, func() {
		logger = mgr.Channel()
		logger.Info("through-fallback-child")
	})

	assert.NotNil(t, logger)
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "ghost", "Warn 应含缺失子通道名 the Warn must carry the missing child name")
	assert.Contains(t, out, "through-fallback-child", "日志应经兜底子通道输出 records must reach stderr via the fallback child")
}

// TestStackChildNamesFromAnySlice 验证 channels 以 []any（TOML/YAML 解码产物）
// 提供且元素均为字符串时同样可用，与 []string 等价。
// TestStackChildNamesFromAnySlice verifies a channels field provided as []any
// (the shape TOML/YAML decoders produce) with all-string elements works
// identically to []string.
func TestStackChildNamesFromAnySlice(t *testing.T) {
	buf1, buf2 := &bytes.Buffer{}, &bytes.Buffer{}
	mgr := log.NewFactory(log.Config{
		Default: "stack",
		Channels: map[string]contracts.Fields{
			"a": {log.KeyDriver: "buffer", log.KeyWriter: buf1, log.KeyLevel: "debug", log.KeyFormat: "console"},
			"b": {log.KeyDriver: "buffer", log.KeyWriter: buf2, log.KeyLevel: "debug", log.KeyFormat: "console"},
			"stack": {
				log.KeyDriver: "stack",
				log.KeyLevel:  "debug",
				"channels":    []any{"a", "b"},
			},
		},
	})
	mgr.Extend("buffer", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		return log.NewBufferLogger(cfg)
	})

	mgr.Channel().Info("from-any-slice")

	assert.Contains(t, buf1.String(), "from-any-slice")
	assert.Contains(t, buf2.String(), "from-any-slice")
}

// fakePlainLogger 是仅实现 contracts.Logger 的测试假驱动产物，按方法名计数，
// 用于类型守卫与 stack 非 zap 子通道用例；并发访问通过互斥锁保护。
// fakePlainLogger is a test fake implementing only contracts.Logger; it counts
// calls per method for the type-guard and stack non-zap child cases, guarded
// by a mutex for concurrent use.
type fakePlainLogger struct {
	mutex sync.Mutex
	calls map[string]int
}

func (f *fakePlainLogger) record(name string) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

// snapshot 返回按方法名计数的副本（五个级别键恒在，未调用即为 0）。
// snapshot returns a copy of the per-method counters (all five level keys are
// always present, zero when never called).
func (f *fakePlainLogger) snapshot() map[string]int {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	copied := map[string]int{"Debug": 0, "Info": 0, "Warn": 0, "Error": 0, "Fatal": 0}
	for name, count := range f.calls {
		copied[name] = count
	}

	return copied
}

func (f *fakePlainLogger) Debug(msg string) { f.record("Debug") }
func (f *fakePlainLogger) Info(msg string)  { f.record("Info") }
func (f *fakePlainLogger) Warn(msg string)  { f.record("Warn") }
func (f *fakePlainLogger) Error(msg string) { f.record("Error") }
func (f *fakePlainLogger) Fatal(msg string) { f.record("Fatal") }

func (f *fakePlainLogger) WithFields(fields contracts.Fields) contracts.Logger { return f }
func (f *fakePlainLogger) WithField(key string, value any) contracts.Logger    { return f }
func (f *fakePlainLogger) WithError(err error) contracts.Logger                { return f }
func (f *fakePlainLogger) WithException(exception contracts.Exception) contracts.Logger {
	return f
}

// 编译期锁定 fakePlainLogger 仅实现 contracts.Logger（不含 Log）。
// Compile-time lock that fakePlainLogger implements only contracts.Logger.
var _ contracts.Logger = (*fakePlainLogger)(nil)

// TestDriverPanicFallsBackAndRecovers 锁定 panic 撤销状态机：认领方在驱动中
// panic 后，构建登记被撤销、等待方被放行拿到可用兜底 logger（不卡死），随后
// 的调用重新构建成功（旧的全锁实现 panic 后可直接重建，语义不得劣化）。
// TestDriverPanicFallsBackAndRecovers locks the panic retraction state machine:
// after the claimant panics inside the driver, the build registration is
// retracted, the waiter is released with a usable fallback logger (never
// wedged), and a later call rebuilds successfully (the old full-lock design
// rebuilt right after a panic, so that property must not regress).
func TestDriverPanicFallsBackAndRecovers(t *testing.T) {
	buf := &bytes.Buffer{}
	release := make(chan struct{})
	started := make(chan struct{})
	var builds int32
	boom := errors.New("driver boom")

	mgr := log.NewFactory(log.Config{
		Default: "app",
		Channels: map[string]contracts.Fields{
			"app": {log.KeyDriver: "slow", log.KeyWriter: buf, log.KeyLevel: "debug", log.KeyFormat: "console"},
		},
	})
	mgr.Extend("slow", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		if atomic.AddInt32(&builds, 1) == 1 {
			close(started)
			<-release // 阻塞认领方，让等待方先落到 <-done hold the claimant so the waiter lands on <-done first
			panic(boom)
		}
		return log.NewBufferLogger(cfg)
	})

	// 认领方：进入驱动后阻塞，放行后 panic（测试内 recover 断言）。
	// Claimant: blocks inside the driver, panics after release (recovered here).
	claimed := make(chan any, 1)
	go func() {
		defer func() { claimed <- recover() }()
		mgr.Channel()
	}()
	<-started

	// 等待方：此刻构建必然已登记，落入等待路径。
	// Waiter: the build is registered by now, so it takes the wait path.
	waited := make(chan contracts.ChannelLogger, 1)
	go func() { waited <- mgr.Channel() }()
	time.Sleep(50 * time.Millisecond)

	// 放行 → 认领方 panic → 撤销登记 + 兜底填充 + close(done)。兜底 logger 在
	// 构建时捕获 stderr，故等待方的放行、兜底 Warn 与可用性断言全部放在同一
	// 捕获窗口内完成。
	// Release -> claimant panics -> registration retracted + fallback filled +
	// close(done). The fallback logger captures stderr at build time, so the
	// waiter's release, the fallback Warn and the usability check all share one
	// capture window.
	var recovered any
	var fallback contracts.ChannelLogger
	out := captureStderr(t, func() {
		close(release)
		recovered = <-claimed

		select {
		case fallback = <-waited:
		case <-time.After(2 * time.Second):
			t.Error("等待方未在 panic 后被放行（构建登记泄漏）waiter was not released after the panic (registry leak)")
		}

		if fallback != nil {
			fallback.Info("after-panic")
		}
	})

	assert.Equal(t, boom, recovered, "认领方应收到驱动 panic the claimant must receive the driver panic")
	assert.NotNil(t, fallback, "等待方应拿到兜底 logger the waiter must receive a fallback logger")
	assert.Contains(t, out, "WARN", "panic 应触发兜底 Warn the panic must emit the fallback Warn")
	assert.Contains(t, out, "after-panic", "兜底 logger 应可用 the fallback must stay usable")

	// 撤销登记后第二次调用重建成功。
	// After the retraction a later call rebuilds successfully.
	rebuilt := mgr.Channel()
	assert.NotNil(t, rebuilt)
	assert.Equal(t, int32(2), atomic.LoadInt32(&builds), "登记已撤销，应重新构建 the registry must have been retracted for a rebuild")
	rebuilt.Info("rebuilt")
	assert.Contains(t, buf.String(), "rebuilt")
}

// TestConcurrentFirstBuildWaitPath 确定性覆盖 singleflight 等待路径：慢驱动
// 阻塞认领方期间，其余 19 个并发调用方全部落在 <-done 等待上（而非事后缓存
// 命中），放行后恰好构建 1 次且全同实例。
// TestConcurrentFirstBuildWaitPath deterministically covers the singleflight
// wait path: while the slow driver blocks the claimant, the other 19
// concurrent callers all land on <-done (rather than later cache hits); after
// release exactly one build happens and every caller shares one instance.
func TestConcurrentFirstBuildWaitPath(t *testing.T) {
	buf := &bytes.Buffer{}
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var builds int32

	mgr := log.NewFactory(log.Config{
		Default: "app",
		Channels: map[string]contracts.Fields{
			"app": {log.KeyDriver: "slow", log.KeyWriter: buf, log.KeyLevel: "debug", log.KeyFormat: "console"},
		},
	})
	mgr.Extend("slow", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		if atomic.AddInt32(&builds, 1) == 1 {
			entered <- struct{}{}
			<-release
		}
		return log.NewBufferLogger(cfg)
	})

	const callers = 20
	instances := make([]contracts.ChannelLogger, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			instances[seq] = mgr.Channel()
		}(i)
	}
	<-entered
	time.Sleep(100 * time.Millisecond) // 其余调用方进入等待 the other callers reach the wait
	close(release)

	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("等待路径卡死 the wait path deadlocked")
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&builds), "并发首建至多构建一次 must build at most once")
	for i := 1; i < callers; i++ {
		assert.Same(t, instances[0], instances[i], "并发调用应共享同一实例 concurrent callers must share one instance")
	}
}

// TestConcurrentStacksShareChildSingleBuild 锁定并发 stack 共享子通道的
// singleflight 语义：两个 goroutine 并发构建引用同一子通道的两个 stack 时，
// 子通道恰好构建 1 次（相交链判定下等待共享，而非私有构建产生双实例）。
// TestConcurrentStacksShareChildSingleBuild locks the singleflight semantics
// of concurrent stacks sharing a child: two goroutines building two stacks
// that reference the same child must trigger exactly one child build (the
// intersecting-chain rule waits and shares instead of privately rebuilding).
func TestConcurrentStacksShareChildSingleBuild(t *testing.T) {
	sharedBuf := &bytes.Buffer{}
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var sharedBuilds int32

	mgr := log.NewFactory(log.Config{
		Default: "p",
		Channels: map[string]contracts.Fields{
			"shared": {log.KeyDriver: "slow", log.KeyWriter: sharedBuf, log.KeyLevel: "debug", log.KeyFormat: "console"},
			"p":      {log.KeyDriver: "stack", log.KeyLevel: "debug", "channels": []string{"shared"}},
			"q":      {log.KeyDriver: "stack", log.KeyLevel: "debug", "channels": []string{"shared"}},
		},
	})
	mgr.Extend("slow", func(name string, cfg contracts.Fields) (contracts.Logger, error) {
		if atomic.AddInt32(&sharedBuilds, 1) == 1 {
			entered <- struct{}{}
			<-release
		}
		return log.NewBufferLogger(cfg)
	})

	pDone := make(chan contracts.ChannelLogger, 1)
	go func() { pDone <- mgr.Channel("p") }()
	<-entered // p 已认领 shared 并阻塞在驱动里 p has claimed shared and blocks inside the driver

	qDone := make(chan contracts.ChannelLogger, 1)
	go func() { qDone <- mgr.Channel("q") }()
	time.Sleep(50 * time.Millisecond) // q 落到等待路径 q reaches the wait path

	close(release)

	select {
	case p := <-pDone:
		assert.NotNil(t, p)
	case <-time.After(2 * time.Second):
		t.Fatal("p 未完成 p did not finish")
	}
	select {
	case q := <-qDone:
		assert.NotNil(t, q)
	case <-time.After(2 * time.Second):
		t.Fatal("q 未完成（不应私有构建也不应死锁）q did not finish (must neither privately rebuild nor deadlock)")
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&sharedBuilds), "共享子通道应恰好构建一次 the shared child must build exactly once")

	// 两个 stack 共享同一子通道实例：经各自 tee 写入同一底层 writer。
	// Both stacks share the same child instance: records reach the shared
	// writer through each stack's own tee.
	mgr.Channel("p").Info("from-p")
	mgr.Channel("q").Info("from-q")
	out := sharedBuf.String()
	assert.Contains(t, out, "from-p")
	assert.Contains(t, out, "from-q")
}

// TestStackMutualReferenceConcurrentNoDeadlock 锁定 I2 判定方案的关键场景：
// X⇄Y 互引的两个 stack 被两个 goroutine 并发构建时不得互等死锁（静态闭包
// 判定识破潜在环，双方私有构建并由 chain 环检测各自兜底 + Warn）。
// TestStackMutualReferenceConcurrentNoDeadlock locks the key scenario behind
// the I2 rule: two goroutines concurrently building mutually referencing
// stacks X and Y must not wait on each other forever (the static closure rule
// exposes the latent cycle, both build privately and fall back + warn through
// the chain-based cycle detection).
func TestStackMutualReferenceConcurrentNoDeadlock(t *testing.T) {
	mgr := log.NewFactory(log.Config{
		Default: "x",
		Channels: map[string]contracts.Fields{
			"x": {log.KeyDriver: "stack", log.KeyLevel: "debug", "channels": []string{"y"}},
			"y": {log.KeyDriver: "stack", log.KeyLevel: "debug", "channels": []string{"x"}},
		},
	})

	var xLogger, yLogger contracts.ChannelLogger
	out := captureStderr(t, func() {
		start := make(chan struct{})
		results := make([]contracts.ChannelLogger, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			results[0] = mgr.Channel("x")
		}()
		go func() {
			defer wg.Done()
			<-start
			results[1] = mgr.Channel("y")
		}()
		close(start)

		finished := make(chan struct{})
		go func() { wg.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("互引 stack 并发构建死锁 concurrent builds of mutually referencing stacks deadlocked")
		}
		xLogger, yLogger = results[0], results[1]
	})

	assert.NotNil(t, xLogger)
	assert.NotNil(t, yLogger)
	assert.Contains(t, out, "循环引用", "潜在环应被识破并兜底告警 the latent cycle must be exposed with a fallback warn")
}
