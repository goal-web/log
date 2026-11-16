// file.go：file 内置驱动的按日切分文件写入器。
//
// file.go: the daily-split file writer of the built-in file driver.
package drivers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/goal-web/contracts"
	"go.uber.org/zap/zapcore"
)

// keyPath 是通道配置中日志文件路径的键，与 log 包的 log.KeyPath（"path"）
// 对应；复刻原因同 keyOutput（子包不能反向引用 log 包）。
// keyPath is the channel key holding the log file path, mirroring log.KeyPath
// ("path") in the log package; replicated for the same reason as keyOutput
// (the subpackage must not import log back).
const keyPath = "path"

// dayFormat 是切分文件名中日期后缀的布局。
// dayFormat is the layout of the date suffix in split file names.
const dayFormat = "20060102"

// DailyFileWriter 是按日切分的文件写入器，实现 zapcore.WriteSyncer（Write+Sync）：
// 每次写入前检查当前日期，日期变化（含首次写入）时关闭旧句柄并切换到
// 前缀-YYYYMMDD.后缀 的新文件。写入全程持有互斥锁，跨天瞬间的并发写安全；
// 不自动 Sync，刷盘时机交给 zapcore（进程退出由 OS 兜底）。
//
// DailyFileWriter is a daily-split file writer implementing
// zapcore.WriteSyncer (Write+Sync): before every write it checks the current
// date and, on change (including the first write), closes the old handle and
// switches to a prefix-YYYYMMDD.suffix file. All writes hold a mutex, so
// concurrent writes across the day boundary are safe; it never Syncs on its
// own — flushing timing belongs to zapcore (the OS covers process exit).
type DailyFileWriter struct {
	mu   sync.Mutex
	path string
	day  string
	file *os.File
	now  func() time.Time
}

// NewDailyFileWriter 创建按日切分的文件写入器。path 为原始日志路径（切分时
// 在其中插入日期后缀，命名规则见 datedPath）；now 注入时钟供测试控制日期，
// 传 nil 时使用 time.Now。
// NewDailyFileWriter creates a daily-split file writer. path is the raw log
// path (the date suffix is inserted into it on split; see datedPath for the
// naming rule); now injects a clock for tests, falling back to time.Now when
// nil.
func NewDailyFileWriter(path string, now func() time.Time) *DailyFileWriter {
	if now == nil {
		now = time.Now
	}

	return &DailyFileWriter{path: path, now: now}
}

// Write 在锁内检查日切并写入：日期变化时先切换文件，再写入当前文件。
// 打开或写入失败时向 stderr 兜底输出一条含路径与错误的记录，然后返回
// error 交给上层（zapcore）处理。
// Write checks the day split under lock and writes: on a date change it
// rotates the file first, then writes to the current file. On an open or
// write failure it emits one fallback line with the path and error to stderr,
// then returns the error to the caller (zapcore).
func (w *DailyFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	day := w.now().Format(dayFormat)
	if w.day != day {
		dated := datedPath(w.path, day)
		if err := w.rotate(day); err != nil {
			fmt.Fprintf(os.Stderr, "log: 打开日志文件 %s 失败 failed to open log file %s: %v\n", dated, dated, err)
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log: 写入日志文件 %s 失败 failed to write log file %s: %v\n", w.file.Name(), w.file.Name(), err)
	}

	return n, err
}

// Sync 透传当前文件的 Sync；尚未打开文件（从未写入）时为空操作。
// Sync passes through to the current file's Sync; it is a no-op when no file
// is open (nothing written yet).
func (w *DailyFileWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}

	return w.file.Sync()
}

// Close 关闭当前文件并清空日期标记，之后的写入会重新按当日建文件。
// 工厂不主动调用（进程退出由 OS 兜底），提供给需要显式回收句柄的场景。
// Close closes the current file and clears the day marker so later writes
// reopen a fresh file for that day. The factory never calls it proactively
// (the OS covers process exit); it serves scenarios needing explicit handle
// reclamation.
func (w *DailyFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}

	err := w.file.Close()
	w.file = nil
	w.day = ""

	return err
}

// datedPath 按日期生成切分后的文件名：去掉原扩展名后拼接 -YYYYMMDD 再还原
// 扩展名。无扩展名时结果同样无扩展名（如 app → app-20261117）。
// datedPath builds the split file name for a day: the original extension is
// stripped, -YYYYMMDD appended, then the extension restored. An
// extension-less path stays extension-less (e.g. app -> app-20261117).
func datedPath(path, day string) string {
	ext := filepath.Ext(path)

	return strings.TrimSuffix(path, ext) + "-" + day + ext
}

// rotate 切换到目标日期的文件：先建目录再打开新文件，全部成功后才关闭旧
// 句柄并替换内部状态——任一步失败时旧状态原样保留（day 未更新，下次写入
// 必然重试切分），不丢旧句柄也不误标日期。
// rotate switches to the target day's file: it creates the directory, opens
// the new file and only then closes the old handle and swaps internal state —
// on any failure the old state is kept untouched (day stays stale, so the
// next write retries the rotation), leaking no handle and mislabeling no day.
func (w *DailyFileWriter) rotate(day string) error {
	dated := datedPath(w.path, day)

	if err := os.MkdirAll(filepath.Dir(dated), 0755); err != nil {
		return err
	}

	file, err := os.OpenFile(dated, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	if w.file != nil {
		_ = w.file.Close()
	}
	w.file = file
	w.day = day

	return nil
}

// NewFileWriter 从通道配置构建 file 驱动的写入器：
//   - path：必填，日志文件原始路径（写入时按日切分为 前缀-YYYYMMDD.后缀）；
//     字段缺失、值为 nil 或类型不是 string 时返回 error。
//
// 时钟使用 time.Now。返回的 *DailyFileWriter 以 zapcore.WriteSyncer 暴露。
// NewFileWriter builds the file driver's writer from the channel config:
//   - path: required, the raw log path (split daily into prefix-YYYYMMDD.suffix
//     on write); a missing field, a nil value or a non-string returns an error.
//
// The clock is time.Now. The returned *DailyFileWriter is exposed as a
// zapcore.WriteSyncer.
func NewFileWriter(cfg contracts.Fields) (zapcore.WriteSyncer, error) {
	path, err := configString(cfg, keyPath)
	if err != nil {
		return nil, err
	}

	if path == "" {
		return nil, fmt.Errorf("log: file 驱动配置缺少 path 字段 file driver config requires a path field")
	}

	return NewDailyFileWriter(path, time.Now), nil
}
