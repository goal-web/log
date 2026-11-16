package tests

import (
	"io"
	"testing"

	apexlog "github.com/apex/log"
	apextext "github.com/apex/log/handlers/text"
	"github.com/goal-web/contracts"

	"github.com/goal-web/log"
)

// benchmarkMessage 是两组基准共用的固定消息，保证消息成本一致。
// benchmarkMessage is the fixed message shared by both benchmark groups,
// keeping the message cost identical.
const benchmarkMessage = "benchmark message"

// 本文件对比 goal-web/log 的 zap 通道与原 apex/log 包装的单条写日志开销。
// 公平性约定（两侧同等工作负载）：
//
//   - 同一消息（benchmarkMessage）+ 同一字段（key=i）+ 都走 Info 级别，
//     且级别过滤均放行（zap 侧通道 debug，apex 侧全局 InfoLevel）；
//   - 输出都不落真实终端：zap 侧 console 编码后写 io.Discard，
//     apex 侧用官方 handlers/text（console 格式的对应物，含真实格式化）
//     写 io.Discard。不用 handlers/discard——它是纯 no-op，
//     会跳过格式化，对比就不再反映“写一条日志”的真实成本；
//   - 构建/logger 装配都在 b.ResetTimer() 之前完成，不计入耗时。
//
// This file compares the per-record cost of the goal-web/log zap channel
// against the legacy apex/log wrapper. Fairness contract (identical workload
// on both sides):
//
//   - same message (benchmarkMessage) + same field (key=i), both at Info
//     level, and both level filters admit the record (zap channel at debug,
//     apex global at InfoLevel);
//   - neither side writes to a real terminal: the zap side encodes with the
//     console encoder into io.Discard, while the apex side uses the official
//     handlers/text (the console-format counterpart, with real formatting)
//     writing into io.Discard. handlers/discard is deliberately avoided —
//     it is a pure no-op that skips formatting entirely, so the comparison
//     would no longer reflect the cost of actually writing a record;
//   - construction/logger assembly happens before b.ResetTimer() and is
//     excluded from the measurement.

// BenchmarkZapChannel 测量 zap 通道管线（console 编码 → io.Discard）的单条开销。
// BenchmarkZapChannel measures the zap channel pipeline (console encoding ->
// io.Discard) per record.
func BenchmarkZapChannel(b *testing.B) {
	// 与内置 console 驱动同一管线（NewBufferLogger）：debug 级别放行 Info 记录。
	// Same pipeline as the built-in console driver (NewBufferLogger): the debug
	// level admits Info records.
	logger, err := log.NewBufferLogger(contracts.Fields{
		log.KeyWriter: io.Discard,
		log.KeyLevel:  "debug",
		log.KeyFormat: "console",
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.WithField("key", i).Info(benchmarkMessage)
	}
}

// BenchmarkApexLogger 测量原 apex/log 包装的管线（包级全局 logger +
// handlers/text → io.Discard）的单条开销，WithField/Info 调用形态与
// supports/logs 的 ApexLogger 一致。
// BenchmarkApexLogger measures the legacy apex/log pipeline (package-global
// logger + handlers/text -> io.Discard) per record, with the same
// WithField/Info call shape as the ApexLogger in supports/logs.
func BenchmarkApexLogger(b *testing.B) {
	// SetHandler/SetLevel 操作包级全局且非并发安全；基准在单 goroutine 内
	// 串行执行，不影响结果。作用域仅限本测试二进制。
	// SetHandler/SetLevel mutate the package global and are not concurrency
	// safe; benchmarks run serially in a single goroutine, so this is fine.
	// The scope is limited to this test binary.
	apexlog.SetHandler(apextext.New(io.Discard))
	apexlog.SetLevel(apexlog.InfoLevel)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		apexlog.WithField("key", i).Info(benchmarkMessage)
	}
}
