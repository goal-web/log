package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goal-web/contracts"
	"github.com/goal-web/log"
	"github.com/goal-web/log/drivers"
	"github.com/stretchr/testify/assert"
)

// TestDailyFileWriterSplit 验证按日切分核心行为：同一 path 跨两天写入时，
// 各日期的内容落到各自独立的 app-YYYYMMDD.log 文件，互不串写。
// TestDailyFileWriterSplit verifies the core daily-split behavior: writes to the
// same path across two days land in separate app-YYYYMMDD.log files.
func TestDailyFileWriterSplit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	day1 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.Local)
	day2 := day1.AddDate(0, 0, 1)

	w := drivers.NewDailyFileWriter(path, func() time.Time { return day1 })
	n, err := w.Write([]byte("day-one\n"))
	assert.NoError(t, err)
	assert.Equal(t, len("day-one\n"), n)

	w2 := drivers.NewDailyFileWriter(path, func() time.Time { return day2 })
	_, err = w2.Write([]byte("day-two\n"))
	assert.NoError(t, err)

	files, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	assert.NoError(t, err)
	assert.Len(t, files, 2) // app-20261117.log / app-20261118.log

	// 内容各归各文件：Glob 按字典序返回，files[0] 是 day1 的文件。
	// Each day's content lands in its own file; Glob returns sorted names,
	// so files[0] belongs to day1.
	b1, err := os.ReadFile(files[0])
	assert.NoError(t, err)
	b2, err := os.ReadFile(files[1])
	assert.NoError(t, err)
	assert.Equal(t, "day-one\n", string(b1))
	assert.Equal(t, "day-two\n", string(b2))
}

// TestDailyFileWriterSameDayReusesFile 验证同一天多次写只产生 1 个文件且内容追加；
// 同日新建的第二个 writer 实例也追加到同一文件（O_APPEND 语义）。
// TestDailyFileWriterSameDayReusesFile verifies same-day writes reuse one file
// with appended content; a second writer instance built on the same day also
// appends to the same file (O_APPEND semantics).
func TestDailyFileWriterSameDayReusesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	day := time.Date(2026, 11, 17, 8, 0, 0, 0, time.Local)

	w := drivers.NewDailyFileWriter(path, func() time.Time { return day })
	_, err := w.Write([]byte("first\n"))
	assert.NoError(t, err)
	_, err = w.Write([]byte("second\n"))
	assert.NoError(t, err)

	// 同日重建 writer 再写，仍落同一文件。
	// Rebuild the writer on the same day; writes still land in the same file.
	w2 := drivers.NewDailyFileWriter(path, func() time.Time { return day })
	_, err = w2.Write([]byte("third\n"))
	assert.NoError(t, err)

	files, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	assert.NoError(t, err)
	assert.Len(t, files, 1)

	content, err := os.ReadFile(files[0])
	assert.NoError(t, err)
	assert.Equal(t, "first\nsecond\nthird\n", string(content))
}

// TestDailyFileWriterNoExt 锁定无扩展名路径的切分命名规则：
// ext := filepath.Ext(path)；dated := TrimSuffix(path, ext) + "-" + day + ext。
// 无扩展名时 ext 为空串，结果为 "app-20261117"（不追加 .log 后缀）。
// TestDailyFileWriterNoExt locks the split naming rule for extension-less
// paths: ext is empty, so the result is "app-20261117" with no .log suffix.
func TestDailyFileWriterNoExt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app")
	day := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)

	w := drivers.NewDailyFileWriter(path, func() time.Time { return day })
	_, err := w.Write([]byte("no-ext\n"))
	assert.NoError(t, err)

	// 精确断言生成的文件名，且目录里没有多余文件。
	// Assert the exact generated file name and no extra files in the dir.
	dated := filepath.Join(dir, "app-20261117")
	content, err := os.ReadFile(dated)
	assert.NoError(t, err)
	assert.Equal(t, "no-ext\n", string(content))

	entries, err := os.ReadDir(dir)
	assert.NoError(t, err)
	assert.Len(t, entries, 1)
}

// TestDailyFileWriterMkdirAll 验证 path 的父目录不存在（嵌套两层）时自动创建后写入成功。
// TestDailyFileWriterMkdirAll verifies missing (two-level nested) parent
// directories are created automatically before writing.
func TestDailyFileWriterMkdirAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "app.log")
	day := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)

	w := drivers.NewDailyFileWriter(path, func() time.Time { return day })
	_, err := w.Write([]byte("mkdir-all\n"))
	assert.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "nested", "deep", "app-20261117.log"))
	assert.NoError(t, err)
	assert.Equal(t, "mkdir-all\n", string(content))
}

// TestDailyFileWriterConcurrentAcrossDayBoundary 验证跨天瞬间的并发写安全：
// 多个 goroutine 共享同一 writer，时钟在写入过程中切换日期；每条消息必须
// 完整落入恰好一个文件（锁内切文件，不允许交错或丢失）。需配合 -race 运行。
// TestDailyFileWriterConcurrentAcrossDayBoundary verifies concurrent writes are
// safe across the day boundary: goroutines share one writer while the injected
// clock flips mid-run; every message must land exactly once in one file
// (rotation under lock, no interleaving or loss). Run with -race.
func TestDailyFileWriterConcurrentAcrossDayBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	day1 := time.Date(2026, 11, 17, 23, 59, 59, 0, time.Local)
	day2 := day1.AddDate(0, 0, 1)

	// now 在前半段返回 day1、后半段返回 day2：每次 Write 恰好调用一次 now，
	// 因此第 flips+1 次写入起切换日期，翻转点确定且落在并发窗口中间。
	// now returns day1 for the first half of calls and day2 afterwards; Write
	// calls now exactly once, so the flip point is deterministic and lands in
	// the middle of the concurrent window.
	const writers, perWriter = 20, 10
	var calls int64
	now := func() time.Time {
		if atomic.AddInt64(&calls, 1) > writers*perWriter/2 {
			return day2
		}
		return day1
	}

	w := drivers.NewDailyFileWriter(path, now)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				// goroutine 内只写入，不做断言（testify 非并发安全），留待汇总后统一校验。
				// goroutines only write; testify is not concurrency-safe, so
				// verification happens after wg.Wait.
				_, _ = w.Write([]byte(fmt.Sprintf("c-%03d-%02d\n", seq, j)))
			}
		}(i)
	}
	wg.Wait()

	// 翻转点在窗口中间，两天必然都产生了文件。
	// The flip lands mid-window, so both days must have produced a file.
	files, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	assert.NoError(t, err)
	assert.Len(t, files, 2)

	var combined strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		assert.NoError(t, err)
		combined.Write(b)
	}

	// 200 条消息每条恰好出现一次：无丢失、无重复、无半行交错。
	// Each of the 200 messages appears exactly once: no loss, no duplication,
	// no torn lines.
	out := combined.String()
	for seq := 0; seq < writers; seq++ {
		for j := 0; j < perWriter; j++ {
			msg := fmt.Sprintf("c-%03d-%02d", seq, j)
			assert.Equal(t, 1, strings.Count(out, msg), "消息 %s 应恰好出现一次 message %s must appear exactly once", msg, msg)
		}
	}
}

// TestFileWriterFromConfig 验证 drivers.NewFileWriter 的配置读取：
// 提供 path 时成功且写入按日切分文件；path 缺失或类型错误时返回 error。
// TestFileWriterFromConfig verifies drivers.NewFileWriter config handling: with
// a path it builds and writes the daily-split file; a missing or mistyped path
// returns an error.
func TestFileWriterFromConfig(t *testing.T) {
	dir := t.TempDir()

	w, err := drivers.NewFileWriter(contracts.Fields{log.KeyPath: filepath.Join(dir, "x.log")})
	assert.NoError(t, err)
	assert.NotNil(t, w)

	_, err = w.Write([]byte("from-config\n"))
	assert.NoError(t, err)

	files, err := filepath.Glob(filepath.Join(dir, "x-*.log"))
	assert.NoError(t, err)
	assert.Len(t, files, 1)
	content, err := os.ReadFile(files[0])
	assert.NoError(t, err)
	assert.Equal(t, "from-config\n", string(content))

	// path 缺失 → error；path 类型错误 → error。
	// Missing path -> error; mistyped path -> error.
	_, err = drivers.NewFileWriter(contracts.Fields{})
	assert.Error(t, err)
	_, err = drivers.NewFileWriter(contracts.Fields{log.KeyPath: 123})
	assert.Error(t, err)
}

// TestFactoryFileDriver 验证工厂到 drivers 的端到端接线：file 通道经
// drivers.NewFileWriter + newChannelLogger 组合后，日志按日切分落盘，
// 级别过滤与通道名注入正常。
// TestFactoryFileDriver verifies the factory-to-drivers wiring end to end: a
// file channel built through drivers.NewFileWriter + newChannelLogger writes
// daily-split files with level filtering and channel-name injection intact.
func TestFactoryFileDriver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.log")

	mgr := log.NewFactory(log.Config{
		Default: "file",
		Channels: map[string]contracts.Fields{
			"file": {log.KeyDriver: "file", log.KeyPath: path, log.KeyLevel: "info", log.KeyFormat: "console"},
		},
	})

	logger := mgr.Channel()
	assert.NotNil(t, logger)

	logger.Info("factory-file-info")
	logger.Debug("factory-file-debug") // 低于 info 级别，应被过滤 below info level, must be filtered

	files, err := filepath.Glob(filepath.Join(dir, "factory-*.log"))
	assert.NoError(t, err)
	assert.Len(t, files, 1)

	content, err := os.ReadFile(files[0])
	assert.NoError(t, err)
	out := string(content)
	assert.Contains(t, out, "factory-file-info")
	assert.NotContains(t, out, "factory-file-debug")
	assert.Contains(t, out, "INFO")
	// 通道名注入：console 编码器在制表符分隔的头部输出通道名。
	// Channel-name injection: the console encoder emits the name in the
	// tab-separated header.
	assert.Contains(t, out, "\tfile\t")
}

// TestFactoryFileDriverMissingPathFallsBack 验证 file 通道缺 path 时驱动构建
// 失败并走兜底通道，经兜底 logger 向 stderr 输出含原因的 Warn。
// TestFactoryFileDriverMissingPathFallsBack verifies a file channel without a
// path fails the driver build and falls back, emitting a Warn with the reason
// to stderr through the fallback logger.
func TestFactoryFileDriverMissingPathFallsBack(t *testing.T) {
	mgr := log.NewFactory(log.Config{
		Default: "file",
		Channels: map[string]contracts.Fields{
			"file": {log.KeyDriver: "file"},
		},
	})

	var logger contracts.ChannelLogger
	out := captureStderr(t, func() {
		logger = mgr.Channel()
	})

	assert.NotNil(t, logger)
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "path")
}

// TestDailyFileWriterCloseThenWriteReopens 验证 Close 后再写的重开语义：
// Close 清空句柄与日期标记，之后的写入按当日重新建文件并以 O_APPEND 追加，
// 内容不丢失也不重复建文件。
// TestDailyFileWriterCloseThenWriteReopens verifies the reopen semantics after
// Close: the day marker and handle are cleared, so the next write recreates
// the current day's file and appends via O_APPEND with nothing lost and no
// duplicate files.
func TestDailyFileWriterCloseThenWriteReopens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	day := time.Date(2026, 11, 17, 10, 0, 0, 0, time.Local)

	w := drivers.NewDailyFileWriter(path, func() time.Time { return day })
	_, err := w.Write([]byte("before-close\n"))
	assert.NoError(t, err)
	assert.NoError(t, w.Close())

	_, err = w.Write([]byte("after-close\n"))
	assert.NoError(t, err)

	files, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	assert.NoError(t, err)
	assert.Len(t, files, 1)

	content, err := os.ReadFile(files[0])
	assert.NoError(t, err)
	assert.Equal(t, "before-close\nafter-close\n", string(content))
}

// TestDailyFileWriterSyncBeforeFirstWrite 验证首写前 Sync 为空操作：不报错、
// 不建文件。
// TestDailyFileWriterSyncBeforeFirstWrite verifies Sync before the first write
// is a no-op: no error and no file created.
func TestDailyFileWriterSyncBeforeFirstWrite(t *testing.T) {
	dir := t.TempDir()

	w := drivers.NewDailyFileWriter(filepath.Join(dir, "app.log"), nil)
	assert.NoError(t, w.Sync(), "首写前 Sync 应为空操作 Sync before the first write must be a no-op")

	files, err := filepath.Glob(filepath.Join(dir, "app-*"))
	assert.NoError(t, err)
	assert.Empty(t, files, "Sync 不得触发建文件 Sync must not create the file")
}

// TestDailyFileWriterRotateFailureKeepsState 验证 rotate 失败时旧状态保留：
// 收回目录写权限使次日打开文件失败后，旧句柄与日期标记均未被破坏——恢复权限
// 后重试即成功，旧文件内容原样、新文件从失败处重新开始。
// TestDailyFileWriterRotateFailureKeepsState verifies the old state survives a
// failed rotate: after revoking the directory's write permission so the next
// day's open fails, neither the old handle nor the day marker is damaged —
// once the permission is restored a retry succeeds, the old file's content is
// intact and the new file starts fresh after the failure.
func TestDailyFileWriterRotateFailureKeepsState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	day1 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.Local)
	day2 := day1.AddDate(0, 0, 1)

	// 可变时钟：首写 day1，之后翻到 day2 触发切分。
	// Mutable clock: the first write happens on day1, then flips to day2 to
	// trigger the rotation.
	current := day1
	w := drivers.NewDailyFileWriter(path, func() time.Time { return current })
	_, err := w.Write([]byte("day-one\n"))
	assert.NoError(t, err)

	// 收回目录写权限：MkdirAll 对已存在目录直接成功，rotate 在 OpenFile 处失败。
	// Revoke the directory's write permission: MkdirAll succeeds on the existing
	// directory, so rotate fails at OpenFile.
	assert.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // 保证 TempDir 可被清理 keep TempDir removable

	current = day2
	_, err = w.Write([]byte("day-two-blocked\n"))
	assert.Error(t, err, "无写权限时次日写入应失败 the next-day write must fail without write permission")

	// 恢复权限后重试：rotate 用保留的旧状态成功切换。
	// After restoring the permission a retry succeeds: rotate switches using the
	// preserved old state.
	assert.NoError(t, os.Chmod(dir, 0o755))
	_, err = w.Write([]byte("day-two-ok\n"))
	assert.NoError(t, err)

	// 旧文件原样保留：失败路径既未误标日期（否则 day-two-ok 会写进旧文件），
	// 也未提前关闭/截断旧句柄。
	// The old file is intact: the failure path neither mislabeled the day
	// (otherwise day-two-ok would land in the old file) nor closed/truncated
	// the old handle early.
	b1, err := os.ReadFile(filepath.Join(dir, "app-20261117.log"))
	assert.NoError(t, err)
	assert.Equal(t, "day-one\n", string(b1))

	// 被阻塞的那条消息不得出现在任何文件中。
	// The blocked message must not appear in any file.
	b2, err := os.ReadFile(filepath.Join(dir, "app-20261118.log"))
	assert.NoError(t, err)
	assert.Equal(t, "day-two-ok\n", string(b2))

	combined := string(b1) + string(b2)
	assert.NotContains(t, combined, "day-two-blocked")
}
