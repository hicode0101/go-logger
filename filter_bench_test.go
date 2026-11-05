package logger

import "testing"

// 级别过滤路径的归因微基准（对应 docs/BENCHMARK.md 第五节）。
//
// 完整过滤路径（BenchmarkFilteredOut_GoLogger，~5.7ns）比 zap（~4.1ns）慢
// 约 1.6ns，本文件把这条路径拆成可归因的三段分别测量：
//
//  1. stdLogger()        包级 API 每次调用需从全局原子指针定位 Logger
//     （zap 基准持局部 *zap.Logger 变量，无此步）；
//  2. formatMessage      legacy API 签名 (f interface{}, v ...interface{})
//     决定消息必须在级别检查【之前】先转成 string
//     （zap 的 Info(msg string) 无预处理）；
//  3. AtomicLevel.Enabled 动态级别的一次原子读，对比 zap 基准用的
//     静态 zapcore.Level（纯整数比较，无原子状态）。
//
// 运行：go test -run=^$ -bench=BenchmarkFilter_ -benchmem
var filterSink int

const filterMsg = "Go's concurrency primitives make it easy to express parallel execution concepts."

// BenchmarkFilter_StdLoad 单测全局定位开销。
func BenchmarkFilter_StdLoad(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if stdLogger() == nil {
			filterSink++
		}
	}
}

// BenchmarkFilter_FormatMsg 单测消息预处理开销（字符串无参数形态）。
func BenchmarkFilter_FormatMsg(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		filterSink += len(formatMessage(filterMsg))
	}
}

// BenchmarkFilter_AtomicLevel 单测动态级别判断（本库实际形态）。
func BenchmarkFilter_AtomicLevel(b *testing.B) {
	al := NewAtomicLevel(ErrorLevel)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !al.Enabled(InfoLevel) {
			filterSink++
		}
	}
}

// BenchmarkFilter_StaticCompare 单测静态级别判断（zap 基准形态）。
// zapcore.Level.Enabled 即纯整数比较；级别与阈值放包级变量，
// 迫使编译器每轮真实访存（与接口调用形态下的取值行为一致）。
var (
	filterLvl = InfoLevel
	filterThr = ErrorLevel
)

func BenchmarkFilter_StaticCompare(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if filterLvl >= filterThr {
			filterSink++
		}
	}
}

// BenchmarkFilter_PackageInfo 与 benchmark 模块 BenchmarkFilteredOut_GoLogger
// 同条件的完整过滤路径（包级 API + error 阈值），作为归因的总量参照。
func BenchmarkFilter_PackageInfo(b *testing.B) {
	if err := SetLevel("error"); err != nil {
		b.Fatal(err)
	}
	defer func() { _ = SetLevel("debug") }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Info(filterMsg)
	}
}

// BenchmarkFilter_InstanceInfo 实例方法路径（无全局定位、无消息预处理）
// 的完整过滤成本，作为加和的交叉验证：应约等于
// Total − StdLoad − FormatMsg。
func BenchmarkFilter_InstanceInfo(b *testing.B) {
	if err := SetLevel("error"); err != nil {
		b.Fatal(err)
	}
	defer func() { _ = SetLevel("debug") }()
	lg := stdLogger()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Info(filterMsg)
	}
}
