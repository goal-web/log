package tests

import (
	"io"
	"os"
	"testing"

	"github.com/goal-web/contracts"
	"github.com/goal-web/log"
	"github.com/goal-web/log/drivers"
	"github.com/stretchr/testify/assert"
)

// captureStdout 临时把 os.Stdout 替换为管道写端并执行 fn，返回期间写入
// stdout 的全部内容；测试结束前恢复原值。实现模式与 captureStderr 一致。
// captureStdout temporarily swaps os.Stdout for a pipe write end while fn runs
// and returns everything written to stdout; the original is restored before the
// test ends. It mirrors the captureStderr implementation.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe 失败 failed: %v", err)
	}

	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original }()

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

// TestConsoleWriterFromConfig 验证 drivers.NewConsoleWriter 的 output 配置：
// 缺省或 stdout（不区分大小写）→ os.Stdout；stderr（不区分大小写）→ os.Stderr；
// 未知值或非字符串类型 → error。
// TestConsoleWriterFromConfig verifies the output config of
// drivers.NewConsoleWriter: missing or stdout (case-insensitive) -> os.Stdout;
// stderr (case-insensitive) -> os.Stderr; unknown values or non-string types
// return an error.
func TestConsoleWriterFromConfig(t *testing.T) {
	// 缺省 output → stdout。
	// Missing output -> stdout.
	out := captureStdout(t, func() {
		w, err := drivers.NewConsoleWriter(contracts.Fields{})
		assert.NoError(t, err)
		_, err = w.Write([]byte("to-stdout\n"))
		assert.NoError(t, err)
	})
	assert.Contains(t, out, "to-stdout")

	// output=stdout（大写，大小写不敏感）→ stdout。
	// output=stdout (uppercase, case-insensitive) -> stdout.
	out = captureStdout(t, func() {
		w, err := drivers.NewConsoleWriter(contracts.Fields{"output": "STDOUT"})
		assert.NoError(t, err)
		_, err = w.Write([]byte("to-stdout-upper\n"))
		assert.NoError(t, err)
	})
	assert.Contains(t, out, "to-stdout-upper")

	// output=stderr → stderr。
	// output=stderr -> stderr.
	errOut := captureStderr(t, func() {
		w, err := drivers.NewConsoleWriter(contracts.Fields{"output": "stderr"})
		assert.NoError(t, err)
		_, err = w.Write([]byte("to-stderr\n"))
		assert.NoError(t, err)
	})
	assert.Contains(t, errOut, "to-stderr")

	// output=STDERR（大小写不敏感）→ stderr。
	// output=STDERR (case-insensitive) -> stderr.
	errOut = captureStderr(t, func() {
		w, err := drivers.NewConsoleWriter(contracts.Fields{"output": "STDERR"})
		assert.NoError(t, err)
		_, err = w.Write([]byte("to-stderr-upper\n"))
		assert.NoError(t, err)
	})
	assert.Contains(t, errOut, "to-stderr-upper")

	// 未知值 → error。
	// Unknown value -> error.
	_, err := drivers.NewConsoleWriter(contracts.Fields{"output": "file"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "file")

	// 类型错误 → error。
	// Wrong type -> error.
	_, err = drivers.NewConsoleWriter(contracts.Fields{log.KeyOutput: 123})
	assert.Error(t, err)
}

// TestFactoryConsoleDriverWritesStdout 锁定 console 内置驱动的缺省输出流为
// stdout（Task 3 时代的临时实现走 stderr 兜底路径，正式驱动后缺省 stdout）。
// TestFactoryConsoleDriverWritesStdout locks the built-in console driver's
// default stream to stdout (the Task 3 temporary implementation reused the
// stderr fallback path; the formal driver defaults to stdout).
func TestFactoryConsoleDriverWritesStdout(t *testing.T) {
	mgr := log.NewFactory(log.Config{
		Default: "console",
		Channels: map[string]contracts.Fields{
			"console": {log.KeyDriver: "console", log.KeyLevel: "debug", log.KeyFormat: "console"},
		},
	})

	out := captureStdout(t, func() {
		mgr.Channel().Info("factory-console-stdout")
	})

	assert.Contains(t, out, "factory-console-stdout")
	assert.Contains(t, out, "INFO")
}
