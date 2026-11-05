// Package benchmark go-logger 与 uber-go/zap 的性能对比基准。
//
// 对比原则（保证公平）：
//  1. 相同的输出目标：全部走 io.Discard（空输出，测纯日志开销）或同一个 os.File；
//  2. 相同的编码格式：JSON 对 JSON、console 对 console，不跨格式比较；
//  3. 相同的内容规模：纯消息 / 10 个结构化字段两种负载；
//  4. 相同的 caller 语义：分组对比"带调用位置"与"不带"两种模式；
//  5. 每组同时对比串行与多核并行（b.RunParallel）吞吐。
//
// 运行方式：
//
//	go test -bench=. -benchmem -count=5 > bench.txt
package benchmark

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	gologger "github.com/hicode0101/go-logger"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ---------------------------------------------------------------------------
// 公共负载与测试设施
// ---------------------------------------------------------------------------

const benchMessage = "Go's concurrency primitives make it easy to express parallel execution concepts."

// fakeError 预构造的 error，避免在循环内分配影响测量。
var fakeError = errors.New("some error")

// fakeTime 预构造的 time 字段值。
var fakeTime = time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

// fakeDuration 预构造的 duration 字段值。
const fakeDuration = 1500 * time.Millisecond

// blackholeW 自定义 sink 的输出目标（各基准重定向，默认丢弃）。
var blackholeW io.Writer = io.Discard

func init() {
	gologger.RegisterSink("testblackhole", func(cfg gologger.SinkConfig) (gologger.Sink, error) {
		return blackholeSink{w: blackholeW}, nil
	})
}

type blackholeSink struct{ w io.Writer }

func (b blackholeSink) Write(p []byte) (int, error) { return b.w.Write(p) }
func (b blackholeSink) Sync() error                 { return nil }
func (b blackholeSink) Close() error                { return nil }

// initGoLogger 按参数初始化全局 go-logger（discard 黑洞输出）。
func initGoLogger(b *testing.B, format string, withCaller bool) {
	b.Helper()
	if err := gologger.Init(gologger.Config{
		Level:             "info",
		Console:           false,
		File:              false,
		DisableCaller:     !withCaller,
		DisableStacktrace: true,
		Sinks:             []gologger.SinkConfig{{Type: "testblackhole", Format: format}},
	}); err != nil {
		b.Fatal(err)
	}
}

// newZapJSON 构造 zap JSON logger（info 级别），caller 可选。
// EncodeTime 用 ISO8601（zap 生产配置默认）。
func newZapJSON(w io.Writer, withCaller bool) *zap.Logger {
	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(cfg),
		zapcore.AddSync(w),
		zapcore.InfoLevel,
	)
	if withCaller {
		return zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	}
	return zap.New(core)
}

// newZapConsole 构造 zap console logger（debug 级别，开发模式形态）。
func newZapConsole(w io.Writer, withCaller bool) *zap.Logger {
	cfg := zap.NewDevelopmentEncoderConfig()
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(cfg),
		zapcore.AddSync(w),
		zapcore.DebugLevel,
	)
	if withCaller {
		return zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	}
	return zap.New(core)
}

// ---------------------------------------------------------------------------
// 一、JSON 编码 · 纯消息（无字段）
// ---------------------------------------------------------------------------

func BenchmarkJSON_MsgOnly_GoLogger(b *testing.B) {
	blackholeW = io.Discard
	initGoLogger(b, "json", false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gologger.Info(benchMessage)
	}
}

func BenchmarkJSON_MsgOnly_Zap(b *testing.B) {
	logger := newZapJSON(io.Discard, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info(benchMessage)
	}
}

func BenchmarkJSON_MsgOnlyCaller_GoLogger(b *testing.B) {
	blackholeW = io.Discard
	initGoLogger(b, "json", true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gologger.Info(benchMessage)
	}
}

func BenchmarkJSON_MsgOnlyCaller_Zap(b *testing.B) {
	logger := newZapJSON(io.Discard, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info(benchMessage)
	}
}

// ---------------------------------------------------------------------------
// 二、JSON 编码 · 10 个结构化字段
// ---------------------------------------------------------------------------

func BenchmarkJSON_10FieldsTyped_GoLogger(b *testing.B) {
	blackholeW = io.Discard
	initGoLogger(b, "json", false)
	log := gologger.GetLogger()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Info(benchMessage,
			gologger.Int("int", 42),
			gologger.Int64("int64", 1234567890),
			gologger.Float64("float64", 3.14159),
			gologger.String("string", "hello world"),
			gologger.Bool("bool", true),
			gologger.Duration("duration", fakeDuration),
			gologger.Time("time", fakeTime),
			gologger.Err(fakeError),
			gologger.String("uuid", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"),
			gologger.String("method", "POST"),
		)
	}
}

func BenchmarkJSON_10Fields_Zap(b *testing.B) {
	logger := newZapJSON(io.Discard, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info(benchMessage,
			zap.Int("int", 42),
			zap.Int64("int64", 1234567890),
			zap.Float64("float64", 3.14159),
			zap.String("string", "hello world"),
			zap.Bool("bool", true),
			zap.Duration("duration", fakeDuration),
			zap.Time("time", fakeTime),
			zap.Error(fakeError),
			zap.String("uuid", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"),
			zap.String("method", "POST"),
		)
	}
}

func BenchmarkJSON_10FieldsKv_GoLogger(b *testing.B) {
	blackholeW = io.Discard
	initGoLogger(b, "json", false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gologger.Infow(benchMessage,
			"int", 42,
			"int64", 1234567890,
			"float64", 3.14159,
			"string", "hello world",
			"bool", true,
			"duration", fakeDuration,
			"time", fakeTime,
			"error", fakeError,
			"uuid", "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			"method", "POST",
		)
	}
}

func BenchmarkJSON_10FieldsKv_ZapSugar(b *testing.B) {
	logger := newZapJSON(io.Discard, false).Sugar()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Infow(benchMessage,
			"int", 42,
			"int64", 1234567890,
			"float64", 3.14159,
			"string", "hello world",
			"bool", true,
			"duration", fakeDuration,
			"time", fakeTime,
			"error", fakeError,
			"uuid", "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			"method", "POST",
		)
	}
}

// ---------------------------------------------------------------------------
// 三、JSON 编码 · 并行多核
// ---------------------------------------------------------------------------

func BenchmarkJSON_MsgOnly_Parallel_GoLogger(b *testing.B) {
	blackholeW = io.Discard
	initGoLogger(b, "json", false)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			gologger.Info(benchMessage)
		}
	})
}

func BenchmarkJSON_MsgOnly_Parallel_Zap(b *testing.B) {
	logger := newZapJSON(io.Discard, false)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			logger.Info(benchMessage)
		}
	})
}

func BenchmarkJSON_10Fields_Parallel_GoLogger(b *testing.B) {
	blackholeW = io.Discard
	initGoLogger(b, "json", false)
	log := gologger.GetLogger()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			log.Info(benchMessage,
				gologger.Int("int", 42),
				gologger.Int64("int64", 1234567890),
				gologger.Float64("float64", 3.14159),
				gologger.String("string", "hello world"),
				gologger.Bool("bool", true),
				gologger.Duration("duration", fakeDuration),
				gologger.Time("time", fakeTime),
				gologger.Err(fakeError),
				gologger.String("uuid", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"),
				gologger.String("method", "POST"),
			)
		}
	})
}

func BenchmarkJSON_10Fields_Parallel_Zap(b *testing.B) {
	logger := newZapJSON(io.Discard, false)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			logger.Info(benchMessage,
				zap.Int("int", 42),
				zap.Int64("int64", 1234567890),
				zap.Float64("float64", 3.14159),
				zap.String("string", "hello world"),
				zap.Bool("bool", true),
				zap.Duration("duration", fakeDuration),
				zap.Time("time", fakeTime),
				zap.Error(fakeError),
				zap.String("uuid", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"),
				zap.String("method", "POST"),
			)
		}
	})
}

// ---------------------------------------------------------------------------
// 四、Console 编码 · 纯消息（人类可读格式对比）
// ---------------------------------------------------------------------------

func BenchmarkConsole_MsgOnly_GoLogger(b *testing.B) {
	blackholeW = io.Discard
	initGoLogger(b, "console", false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gologger.Info(benchMessage)
	}
}

func BenchmarkConsole_MsgOnly_Zap(b *testing.B) {
	logger := newZapConsole(io.Discard, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info(benchMessage)
	}
}

func BenchmarkConsole_MsgOnly_ZapSugar(b *testing.B) {
	logger := newZapConsole(io.Discard, true).Sugar()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info(benchMessage)
	}
}

func BenchmarkConsole_MsgOnly_StdlibLog(b *testing.B) {
	l := log.New(io.Discard, "", 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Printf("%s", benchMessage)
	}
}

// ---------------------------------------------------------------------------
// 五、写真实文件（go-logger 内建滚动切割器 Rotator 作为公共载体之一）
// ---------------------------------------------------------------------------

func BenchmarkFile_GoLogger_Rotator(b *testing.B) {
	dir := b.TempDir()
	if err := gologger.Init(gologger.Config{
		Level:             "info",
		Console:           false,
		File:              false,
		DisableCaller:     true,
		DisableStacktrace: true,
		Sinks: []gologger.SinkConfig{
			{Type: "file", Format: "json", Path: filepath.Join(dir, "app.log"), MaxSizeMB: 1024},
		},
	}); err != nil {
		b.Fatal(err)
	}
	defer func() { _ = gologger.Close() }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gologger.Info(benchMessage)
	}
}

func BenchmarkFile_GoLogger_RotatorBuffered(b *testing.B) {
	dir := b.TempDir()
	if err := gologger.Init(gologger.Config{
		Level:             "info",
		Console:           false,
		File:              false,
		DisableCaller:     true,
		DisableStacktrace: true,
		Sinks: []gologger.SinkConfig{
			{Type: "file", Format: "json", Path: filepath.Join(dir, "app.log"),
				MaxSizeMB: 1024, BufferSizeKB: 64},
		},
	}); err != nil {
		b.Fatal(err)
	}
	defer func() { _ = gologger.Close() }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gologger.Info(benchMessage)
	}
}

func BenchmarkFile_Zap_OsFile(b *testing.B) {
	dir := b.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	logger := newZapJSON(f, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info(benchMessage)
	}
}

func BenchmarkFile_Zap_ViaRotator(b *testing.B) {
	dir := b.TempDir()
	rot, err := gologger.NewRotator(filepath.Join(dir, "app.log"), 1024, 10, 7, false, true)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = rot.Close() }()
	logger := newZapJSON(rot, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info(benchMessage)
	}
}

// newZapFilteredOut 构造核心级别为 error 的 zap（info 消息全部被过滤）。
func newZapFilteredOut() *zap.Logger {
	cfg := zap.NewProductionEncoderConfig()
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(cfg),
		zapcore.AddSync(io.Discard),
		zapcore.ErrorLevel,
	)
	return zap.New(core)
}

// ---------------------------------------------------------------------------
// 六、级别过滤（禁用日志的零成本验证：error 阈值 + Info 调用）
// ---------------------------------------------------------------------------

func BenchmarkFilteredOut_GoLogger(b *testing.B) {
	blackholeW = io.Discard
	if err := gologger.Init(gologger.Config{
		Level:   "error",
		Console: false,
		File:    false,
		Sinks:   []gologger.SinkConfig{{Type: "testblackhole", Format: "console"}},
	}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gologger.Info(benchMessage) // 全部被过滤
	}
}

func BenchmarkFilteredOut_Zap(b *testing.B) {
	logger := newZapFilteredOut()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info(benchMessage) // error 阈值下全部被过滤
	}
}
