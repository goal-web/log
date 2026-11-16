package log

import (
	"fmt"
	"sync"

	"github.com/goal-web/contracts"
	"github.com/goal-web/log/drivers"
	"github.com/goal-web/log/internal/configutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// driverStack 是内置组合通道的保留驱动名：配置里 driver = "stack" 时工厂走
// stack 组合分支（zapcore.NewTee 扇出），该名字不可被自定义驱动覆盖构建行为。
// driverStack is the reserved driver name of the built-in composition channel:
// driver = "stack" routes the factory into the stack branch (zapcore.NewTee
// fan-out); custom drivers cannot override its build behavior.
const driverStack = "stack"

// fallbackChannelName 是兜底通道名：注入 Named("fallback") 后，
// 兜底流量在多通道输出中可区分。
// fallbackChannelName is the fallback channel name: injected via
// Named("fallback") so fallback traffic is distinguishable in multi-channel
// output.
const fallbackChannelName = "fallback"

// Factory 是 contracts.LogManager 的实现：按名称懒构建通道并缓存，
// 支持运行时注册自定义驱动，并在零配置或通道缺失时兜底。
// 通道解析与并发控制（缓存 + singleflight）见 manager.go；本文件只管构建。
// Factory is a contracts.LogManager: channels are built lazily by name and
// cached, custom drivers can be registered at runtime, and missing or empty
// configurations fall back to a built-in channel. Channel resolution and
// concurrency (cache + singleflight) live in manager.go; this file only
// builds.
type Factory struct {
	config   Config
	drivers  map[string]contracts.LogDriver
	channels map[string]contracts.ChannelLogger
	builds   map[string]*channelBuild
	// stackClosure 是每个通道经静态 stack 依赖边可达的通道集合（含自身），
	// NewFactory 时从配置一次性算出（配置此后不可变），供并发构建的死锁判定。
	// stackClosure is the set of channels reachable from each channel over the
	// static stack dependency edges (including itself), computed once from the
	// configuration at NewFactory (the config is immutable afterwards) for the
	// concurrent-build deadlock check.
	stackClosure map[string]map[string]bool
	mutex        sync.Mutex
}

// NewFactory 用给定配置创建 LogManager。配置完全为空（无默认通道且无通道表）
// 时注入内置 console/debug 通道，保证开箱即用；通道表可由 ParseConfig 生成。
//
// 调用方传入 Config 后不得再修改它（尤其是 Channels map）：工厂据此静态快照
// 计算 stack 依赖闭包做并发构建的死锁防护，事后改动不会被感知。
//
// NewFactory creates a LogManager with the given configuration. An entirely
// empty configuration (no default channel and no channel table) gets the
// built-in console/debug channel injected so the component works out of the
// box; the channel table may come from ParseConfig.
//
// The caller must not mutate the passed Config after NewFactory returns (its
// Channels map in particular): the factory computes the static stack
// dependency closure from this snapshot for concurrent-build deadlock
// protection, and later mutations go unnoticed.
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
		config:       config,
		drivers:      newBuiltinDrivers(),
		channels:     map[string]contracts.ChannelLogger{},
		builds:       map[string]*channelBuild{},
		stackClosure: buildStackClosure(config.Channels),
	}
}

// newBuiltinDrivers 返回内置驱动表：console（stdout/stderr，output 控制）
// 与 file（按日切分，path 控制）。drivers 子包只提供 writer（不能反向引用
// log 包），writer + encoder + level → logger 的组合在此完成；返回的
// Logger 实际为 ChannelLogger（zap 适配实现），兜底分派见 finalizeChannel。
// newBuiltinDrivers returns the built-in driver table: console
// (stdout/stderr, via output) and file (daily-split, via path). The drivers
// subpackage provides writers only (it must not import log back), so the
// writer + encoder + level -> logger composition happens here; the returned
// Logger is in fact a ChannelLogger (zap-backed), with the fallback dispatch
// in finalizeChannel.
func newBuiltinDrivers() map[string]contracts.LogDriver {
	return map[string]contracts.LogDriver{
		"console": consoleDriver,
		"file":    fileDriver,
	}
}

// consoleDriver 是内置 console 驱动的构建实现：按 output 配置取标准流，
// 再经 newChannelFromWriter 组装。兜底通道（fallbackChannel）复用同一管线，
// 保证兜底行为与正式 console 通道一致。
// consoleDriver is the built-in console driver's build implementation: it
// picks the standard stream by the output config and assembles through
// newChannelFromWriter. The fallback channel (fallbackChannel) reuses the
// same pipeline, keeping fallback behavior identical to a formal console
// channel.
func consoleDriver(_ string, cfg contracts.Fields) (contracts.Logger, error) {
	writer, err := drivers.NewConsoleWriter(cfg)
	if err != nil {
		return nil, err
	}

	return newChannelFromWriter(cfg, writer)
}

// fileDriver 是内置 file 驱动的构建实现：按 path 配置取按日切分写入器，
// 再经 newChannelFromWriter 组装。
// fileDriver is the built-in file driver's build implementation: it builds
// the daily-split writer from the path config and assembles through
// newChannelFromWriter.
func fileDriver(_ string, cfg contracts.Fields) (contracts.Logger, error) {
	writer, err := drivers.NewFileWriter(cfg)
	if err != nil {
		return nil, err
	}

	return newChannelFromWriter(cfg, writer)
}

// newChannelFromWriter 用通道配置中的 level 与 format，把 driver 提供的
// writer 组装为 zap 通道 logger，是内置驱动共用的组合管线（NewBufferLogger
// 亦复用）。级别或格式非法时返回 error。
// newChannelFromWriter assembles a writer from a driver into a zap channel
// logger using the level and format from the channel config; it is the shared
// composition pipeline for the built-in drivers (reused by NewBufferLogger
// too). It returns an error on an invalid level or format.
func newChannelFromWriter(cfg contracts.Fields, writer zapcore.WriteSyncer) (contracts.Logger, error) {
	level, err := configutil.String(cfg, KeyLevel)
	if err != nil {
		return nil, err
	}

	format, err := configutil.String(cfg, KeyFormat)
	if err != nil {
		return nil, err
	}

	return newChannelLogger(writer, level, format)
}

// buildChannel 按通道配置构建通道实例（调用方已确认缓存未命中）：
//   - 通道未配置：名为空（default 缺失）时发出专属的 default 未配置提示，
//     其余按通道缺失兜底；均经 stderr 兜底 logger 发出 Warn 后返回兜底通道；
//   - driver = "stack"：走 buildStackChannel 递归组合（环检测经 chain）；
//   - 其余驱动：调用注册的驱动构建，失败兜底。
//
// chain 是当前 goroutine 正在构建的通道集合（含 channelName 自身），用于
// stack 递归的循环引用检测。
//
// buildChannel builds a channel instance from its config (the caller has
// already established a cache miss):
//   - unconfigured channel: an empty name (missing default) gets the
//     dedicated default-missing warning, anything else the channel-missing
//     fallback; both warn through the stderr fallback logger;
//   - driver = "stack": composes recursively via buildStackChannel (cycle
//     detection through chain);
//   - any other driver: built through the registered driver, falling back on
//     error.
//
// chain is the set of channels currently being built on this goroutine
// (including channelName itself), used for the stack's circular-reference
// detection.
func (f *Factory) buildChannel(channelName string, chain map[string]bool) contracts.ChannelLogger {
	channelConfig, ok := f.config.Channels[channelName]
	if !ok {
		if channelName == "" {
			return fallbackChannel(errDefaultChannelMissing)
		}

		return fallbackChannel(fmt.Errorf(
			"log: 日志通道 %q 未配置 log channel %q is not configured", channelName, channelName,
		))
	}

	driverName, _ := channelConfig[KeyDriver].(string)

	if driverName == driverStack {
		logger, err := f.buildStackChannel(channelName, channelConfig, chain)
		if err != nil {
			return fallbackChannel(err)
		}

		return f.finalizeChannel(logger, channelName)
	}

	// 驱动表读取与 Extend 的写入同锁：构建在锁外执行，不加锁会与运行时
	// 注册并发冲突（map 竞态）。LogDriver 函数值不可变，取引用后锁外调用安全。
	// The driver table is read under the same lock Extend writes under: builds
	// run outside the lock, so an unguarded read would race runtime
	// registration (map race). LogDriver values are immutable, so calling the
	// fetched reference outside the lock is safe.
	f.mutex.Lock()
	driver, ok := f.drivers[driverName]
	f.mutex.Unlock()

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

	return f.finalizeChannel(logger, channelName)
}

// buildStackChannel 组合 stack 通道：解析 channels 子通道名，逐个递归构建
// （子通道复用 manager 的缓存/构建管线），收集各子通道的 zapcore.Core 后
// zapcore.NewTee 扇出，最后按 stack 自身 level 在 tee 之后统一过滤
// （levelFilteredCore）。
//
// 约束与错误处理（均返回 error，由 buildChannel 统一兜底 + Warn）：
//   - channels 字段缺失、类型不是 []string/[]any、元素非字符串或列表为空：
//     配置错误（[]any 为 TOML/YAML 解码产物，与 []string 等价接受）；
//   - 子通道命中 chain：循环引用错误；
//   - 子通道产物不是 zap 适配实现：无法参与 NewTee 组合，配置错误。
//
// channels 列表中的重复子通道不去重：同一子通道的 core 会在 tee 中出现多次，
// 同一条日志向该子通道重复写入（双写），按配置语义直译，不做特殊处理。
// Duplicate child names in channels are not deduplicated: the same child's
// core appears in the tee multiple times and one record is written to it
// repeatedly (double-write) — taken literally from the configuration.
//
// buildStackChannel composes a stack channel: it parses the channels child
// names, builds each one recursively (children reuse the manager's cache and
// build pipeline), collects every child's zapcore.Core, fans out with
// zapcore.NewTee, and finally applies the stack's own level uniformly after
// the tee (levelFilteredCore).
//
// Constraints and error handling (all return an error; buildChannel falls
// back + warns uniformly):
//   - a channels field that is missing, not []string/[]any, has non-string
//     elements, or is empty is a configuration error ([]any is what TOML/YAML
//     decoders produce and is accepted as equivalent to []string);
//   - a child hitting chain is a circular-reference error;
//   - a child product that is not the zap adapter cannot join the NewTee
//     composition and is a configuration error.
func (f *Factory) buildStackChannel(channelName string, cfg contracts.Fields, chain map[string]bool) (contracts.ChannelLogger, error) {
	children, err := stackChildNames(channelName, cfg)
	if err != nil {
		return nil, err
	}

	cores := make([]zapcore.Core, 0, len(children))
	for _, child := range children {
		if chain[child] {
			return nil, fmt.Errorf(
				"log: stack 通道 %q 循环引用子通道 %q stack channel %q circularly references child %q",
				channelName, child, channelName, child,
			)
		}

		childLogger := f.getOrBuildChannel(child, chain)

		// 只有 zap 适配实现持有可 tee 的 core；纯 Logger 驱动无法参与组合。
		// Only the zap adapter holds a teeable core; plain Logger drivers
		// cannot join the composition.
		zl, ok := childLogger.(*zapLogger)
		if !ok {
			return nil, fmt.Errorf(
				"log: stack 通道 %q 的子通道 %q 非 zap 实现，无法参与组合 stack channel %q child %q is not zap-backed and cannot be composed",
				channelName, child, channelName, child,
			)
		}

		cores = append(cores, zl.core)
	}

	// 子通道各自的 level 过滤已内建于各 core；stack 自身 level 在 tee 后统一。
	// Each child's own level filter is built into its core; the stack's own
	// level applies uniformly after the tee.
	core := zapcore.NewTee(cores...)

	levelStr, err := configutil.String(cfg, KeyLevel)
	if err != nil {
		return nil, err
	}

	level, err := parseLevel(levelStr)
	if err != nil {
		return nil, err
	}

	if level != zapcore.DebugLevel {
		core = &levelFilteredCore{Core: core, level: level}
	}

	return &zapLogger{core: core, l: zap.New(core)}, nil
}

// levelFilteredCore 在任意 core 之上叠加下限级别过滤，供 stack 自身 level 在
// tee 之后统一过滤：低于下限的 entry 在 Check 处短路，不进入任何子通道 core，
// 也不产生格式化开销。过滤内建于 core（而非 zap.Logger 选项），保证 stack 作
// 为更高层 stack 的子通道时过滤语义随之传递。
// levelFilteredCore adds a floor-level filter on top of any core, letting a
// stack apply its own level uniformly after the tee: entries below the floor
// short-circuit at Check, never reaching any child core and paying no encoding
// cost. The filter lives in the core (not a zap.Logger option) so the filter
// semantics propagate when the stack itself becomes a child of a higher stack.
type levelFilteredCore struct {
	zapcore.Core
	level zapcore.Level
}

// Level 返回过滤下限，如实反映组合后的生效级别。
// Level returns the filter floor, faithfully reflecting the effective level.
func (c *levelFilteredCore) Level() zapcore.Level {
	return c.level
}

// Check 先按下限短路，未过滤的 entry 交由内层 core 检查。
// Check short-circuits on the floor first, then delegates to the inner core.
func (c *levelFilteredCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if entry.Level < c.level {
		return checked
	}

	return c.Core.Check(entry, checked)
}

// stackChildNames 解析 stack 通道的 channels 子通道名列表：接受 []string 与
// 全字符串元素的 []any（TOML/YAML 解码产物）；其余形态（缺失、类型不符、
// 元素非字符串、空列表）返回配置错误。
// stackChildNames parses the stack channel's channels child-name list: it
// accepts []string and []any with all-string elements (the TOML/YAML decoder
// shape); any other form (missing, wrong type, non-string element, empty
// list) is a configuration error.
func stackChildNames(channelName string, cfg contracts.Fields) ([]string, error) {
	raw, ok := cfg[KeyChannels]
	if !ok || raw == nil {
		return nil, fmt.Errorf(
			"log: stack 通道 %q 需提供 channels 字段 stack channel %q requires a channels field",
			channelName, channelName,
		)
	}

	switch value := raw.(type) {
	case []string:
		if len(value) == 0 {
			return nil, fmt.Errorf(
				"log: stack 通道 %q 未配置任何子通道 stack channel %q has no child channels",
				channelName, channelName,
			)
		}

		return value, nil
	case []any:
		names := make([]string, 0, len(value))
		for i, item := range value {
			name, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf(
					"log: stack 通道 %q 的 channels[%d] 需为字符串，实际类型 %T stack channel %q channels[%d] must be a string, got %T",
					channelName, i, item, channelName, i, item,
				)
			}
			names = append(names, name)
		}

		if len(names) == 0 {
			return nil, fmt.Errorf(
				"log: stack 通道 %q 未配置任何子通道 stack channel %q has no child channels",
				channelName, channelName,
			)
		}

		return names, nil
	default:
		return nil, fmt.Errorf(
			"log: stack 通道 %q 的 channels 字段需为字符串数组，实际类型 %T stack channel %q channels field must be a string slice, got %T",
			channelName, raw, channelName, raw,
		)
	}
}

// buildStackClosure 计算每个通道经静态 stack 依赖边可达的通道集合（含自身）：
// 从 NewFactory 的配置出发迭代到不动点（子通道引用按 stackChildNames 解析，
// 非法配置按无子通道处理，运行时自会兜底）。不动点天然覆盖静态环（SCC 成员
// 互相可达），终止性由单调增长 + 有限通道集保证。仅 stack 驱动产生依赖边；
// 驱动构建期间的重入（见 Extend 注释）不在此列。
// buildStackClosure computes the set of channels reachable from each channel
// over the static stack dependency edges (including itself): it iterates from
// the NewFactory configuration to a fixpoint (child references are parsed via
// stackChildNames; an invalid config counts as no children — the runtime falls
// back anyway). The fixpoint naturally covers static cycles (SCC members reach
// each other) and terminates because the sets grow monotonically over a finite
// channel universe. Only the stack driver produces edges; re-entrant calls
// made by drivers during their build are not covered (see Extend).
func buildStackClosure(channels map[string]contracts.Fields) map[string]map[string]bool {
	closures := make(map[string]map[string]bool, len(channels))
	for name := range channels {
		closures[name] = map[string]bool{name: true}
	}

	childrenOf := make(map[string][]string, len(channels))
	for name, cfg := range channels {
		if driverName, _ := cfg[KeyDriver].(string); driverName == driverStack {
			if children, err := stackChildNames(name, cfg); err == nil {
				childrenOf[name] = children
			}
		}
	}

	for changed := true; changed; {
		changed = false
		for name, children := range childrenOf {
			for _, child := range children {
				for reachable := range closures[child] {
					if !closures[name][reachable] {
						closures[name][reachable] = true
						changed = true
					}
				}
			}
		}
	}

	return closures
}

// stackClosureIntersects 判断正在构建的通道按静态闭包是否可能回归到 chain 中
// 的某个祖先通道（即调用方 goroutine 当前持有的构建登记）：相交即可能与祖先
// 所在 goroutine 形成互等死锁，调用方必须私有构建。
// stackClosureIntersects reports whether the channel being built could reach
// back to an ancestor in chain per the static closure (i.e. a build
// registration the caller's goroutine currently holds): an intersection means
// the two goroutines could wait on each other forever, so the caller must
// build privately.
func (f *Factory) stackClosureIntersects(channelName string, chain map[string]bool) bool {
	closure := f.stackClosure[channelName]
	for name := range chain {
		if closure[name] {
			return true
		}
	}

	return false
}

// finalizeChannel 完成通道构建的收尾：注入通道名（zap 适配实现写入 encoder
// 的 channel 键），随后做类型守卫——产物已是 ChannelLogger 直接用；仅实现
// Logger 的驱动按级别分派包装为 ChannelLogger。
// finalizeChannel finishes a channel build: it injects the channel name (the
// zap adapter writes it to the encoder's channel key), then applies the type
// guard — a ChannelLogger product is used as-is, while a driver that only
// implements Logger is wrapped into a ChannelLogger dispatching on level.
func (f *Factory) finalizeChannel(logger contracts.Logger, channelName string) contracts.ChannelLogger {
	logger = withChannelName(logger, channelName)

	if channelLogger, ok := logger.(contracts.ChannelLogger); ok {
		return channelLogger
	}

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

// fallbackChannel 构建兜底 logger：走内置 console 驱动管线（output=stderr，
// level/format 取默认 debug/console），并注入 Named("fallback") 区分兜底流量，
// 最后经兜底 logger 输出构建失败的原因。兜底不再直连 os.Stderr，而是与正式
// console 通道完全同管线，行为（stderr/debug/console）保持不变。
// fallbackChannel builds the fallback logger: it goes through the built-in
// console driver pipeline (output=stderr, level/format defaulting to
// debug/console) and injects Named("fallback") to distinguish fallback
// traffic, then reports why the original channel failed through the fallback
// logger itself. The fallback no longer wires os.Stderr directly but uses
// exactly the formal console channel pipeline, with unchanged behavior
// (stderr/debug/console).
func fallbackChannel(reason error) contracts.ChannelLogger {
	logger, err := consoleDriver(fallbackChannelName, contracts.Fields{KeyOutput: "stderr"})
	if err != nil {
		// 兜底构建几乎不可能失败（stderr/debug/console 全为内置默认值），失败时
		// 静默降级为空 logger，保证调用方永不拿到 nil。
		// The fallback build is nearly impossible to fail (stderr/debug/console are
		// all built-in defaults); degrade silently to a nop logger so callers never
		// receive nil.
		nopCore := zapcore.NewNopCore()
		return &zapLogger{core: nopCore, l: zap.New(nopCore)}
	}

	// Named("fallback")：兜底流量在多通道输出中可区分（Task 4 审查遗留）。
	// Named("fallback"): fallback traffic is distinguishable in multi-channel
	// output (a Task 4 review leftover).
	logger = withChannelName(logger, fallbackChannelName)

	channelLogger, ok := logger.(contracts.ChannelLogger)
	if !ok {
		// console 管线产物恒为 zap 适配实现（ChannelLogger），此分支纯防御。
		// The console pipeline always yields the zap adapter (a ChannelLogger);
		// this branch is purely defensive.
		channelLogger = &levelDispatchLogger{logger: logger}
	}

	channelLogger.Warn("log 通道构建失败，已回退到 stderr/console/debug: " + reason.Error())

	return channelLogger
}
