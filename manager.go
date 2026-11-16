package log

// manager.go：通道解析与并发控制——Channel 的缓存复用、singleflight 式首次
// 构建合并（互斥锁只保护缓存/驱动表/构建登记的读写，驱动构建/递归在锁外
// 进行），以及运行时自定义驱动注册。构建本体（stack 组合、兜底等）见
// factory.go。
//
// manager.go: channel resolution and concurrency — Channel's cache reuse,
// singleflight-style first-build coalescing (the mutex only guards cache,
// driver-table and build-registry reads/writes; driver builds and recursion
// run outside the lock), and runtime custom driver registration. The build
// itself (stack composition, fallbacks, etc.) lives in factory.go.

import (
	"fmt"

	"github.com/goal-web/contracts"
)

// channelBuild 记录一次进行中的通道构建：done 在构建完成时关闭，logger 在
// 关闭前写入——等待方在 <-done 之后读取即可安全共享结果（channel 关闭的
// happens-before 保证）。
// channelBuild tracks one in-flight channel build: done is closed on
// completion with logger written beforehand — waiters read it after <-done,
// which the channel-close happens-before edge makes safe.
type channelBuild struct {
	done   chan struct{}
	logger contracts.ChannelLogger
}

// Channel 返回给定通道的 logger；不传名称时返回默认通道。
// Channel returns the logger for the given channel; no name means the default channel.
func (f *Factory) Channel(name ...string) contracts.ChannelLogger {
	channelName := f.config.Default
	if len(name) > 0 && name[0] != "" {
		channelName = name[0]
	}

	return f.getOrBuildChannel(channelName, nil)
}

// Extend 以给定名称注册自定义日志驱动。
//
// 注意 "stack" 为内置组合通道的保留驱动名：注册同名驱动只会覆盖驱动表条目，
// 构建行为仍走 stack 组合分支。
//
// Extend registers a custom log driver under the given name.
//
// Note that "stack" is the reserved name of the built-in composition channel:
// registering a driver under that name only overwrites the driver table entry,
// while building still routes through the stack composition branch.
func (f *Factory) Extend(name string, driver contracts.LogDriver) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	f.drivers[name] = driver
}

// getOrBuildChannel 返回通道实例（Channel 与 stack 子通道递归共用）：
//   - 缓存命中：直接复用；
//   - 同名构建进行中且调用方不在构建链上：等待该构建完成后共享其结果
//     （singleflight，首次并发构建不重复）；
//   - 同名构建进行中且调用方自身处于构建链（stack 递归）中：不再等待——
//     跨 goroutine 的相互引用若互相等待会死锁——改为私有构建（不写缓存、
//     不登记构建），环检测仍由 chain 兜住；
//   - 否则认领本次构建，锁外执行 buildChannel，完成后写缓存并广播。
//
// chain 是调用方正在构建中的通道集合（stack 递归环检测），可为 nil；
// 传入的 map 不会被修改，子构建收到的是加上了 channelName 的副本。
//
// getOrBuildChannel returns the channel instance (shared by Channel and the
// stack's child recursion):
//   - cache hit: reused directly;
//   - an in-flight build of the same name while the caller is not itself
//     building: wait for that build and share its result (singleflight, so
//     first concurrent builds do not duplicate work);
//   - an in-flight build of the same name while the caller is inside a build
//     chain (stack recursion): do not wait — mutual cross-goroutine references
//     waiting on each other would deadlock — and build privately instead (no
//     cache write, no build registration), with cycle detection still enforced
//     via chain;
//   - otherwise claim the build, run buildChannel outside the lock, then
//     publish to the cache and broadcast. A panicked build retracts the
//     registration and releases its waiters with the fallback logger, so one
//     panic cannot wedge the manager.
//
// chain is the set of channels the caller is currently building (the stack's
// cycle detection) and may be nil; the passed map is never mutated — child
// builds receive a copy extended with channelName.
func (f *Factory) getOrBuildChannel(channelName string, chain map[string]bool) contracts.ChannelLogger {
	f.mutex.Lock()

	if cached, ok := f.channels[channelName]; ok {
		f.mutex.Unlock()
		return cached
	}

	build, building := f.builds[channelName]
	switch {
	case building && len(chain) > 0:
		// 递归构建中遇到他方正在构建的同名通道：私有构建规避跨 goroutine 死锁。
		// Inside a recursive build meeting another goroutine's in-flight build of
		// the same channel: build privately to avoid a cross-goroutine deadlock.
		f.mutex.Unlock()

		return f.buildChannel(channelName, withChannelInChain(chain, channelName))
	case building:
		f.mutex.Unlock()

		<-build.done
		return build.logger
	default:
		build = &channelBuild{done: make(chan struct{})}
		f.builds[channelName] = build
		f.mutex.Unlock()

		// 无论正常完成还是 panic：撤销构建登记并放行等待方。panic 时 logger
		// 尚未写入，等待方拿到兜底 logger，避免构建一次 panic 就让后续同名
		// 请求永远等待（旧的全锁实现 panic 后下次调用可直接重建，语义不劣化）。
		// Whether the build completes normally or panics: retract the build
		// registration and release the waiters. On a panic the logger is unset,
		// so waiters receive the fallback logger — one panicked build must not
		// wedge every later same-name request forever (the old full-lock design
		// simply rebuilt on the next call, so this preserves that property).
		defer func() {
			f.mutex.Lock()
			delete(f.builds, channelName)
			f.mutex.Unlock()

			if build.logger == nil {
				build.logger = fallbackChannel(fmt.Errorf(
					"log: 通道 %q 构建过程中 panic channel %q build panicked", channelName, channelName,
				))
			}

			close(build.done)
		}()
	}

	logger := f.buildChannel(channelName, withChannelInChain(chain, channelName))

	f.mutex.Lock()
	f.channels[channelName] = logger
	f.mutex.Unlock()

	build.logger = logger

	return logger
}

// withChannelInChain 返回把 channelName 加入 chain 的副本；chain 为 nil 时
// 返回只含 channelName 的新集合。原 map 不被修改，保证兄弟子构建的环检测
// 互不污染（例如 stack [a, b] 且 b 又引用 a 时，a 已构建完成不应被视为环）。
// withChannelInChain returns a copy of chain with channelName added; a nil
// chain yields a fresh set containing only channelName. The original map is
// never mutated so sibling child builds keep independent cycle detection (e.g.
// for stack [a, b] where b references a again, the finished a must not be
// mistaken for a cycle).
func withChannelInChain(chain map[string]bool, channelName string) map[string]bool {
	next := make(map[string]bool, len(chain)+1)
	for name := range chain {
		next[name] = true
	}
	next[channelName] = true

	return next
}
