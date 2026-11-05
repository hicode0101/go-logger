# go-logger v2 与 uber-go/zap 性能对比基准报告

- **测试日期**：2026-11-05
- **硬件**：Intel Core i7-12700H（12 代酷睿，20 线程）、Windows 11（win32 x64）
- **Go 版本**：go1.25.1 windows/amd64
- **对比版本**：go-logger v2（本库，纯标准库）vs uber-go/zap v1.28.0
- **方法**：`go test -bench=. -benchmem -count=5 -benchtime=1s`，下表数值为 5 轮均值（ns/op，越小越好；allocs/op 为每条日志的堆分配次数）
- **公平性保障**：同机同负载；JSON 对 JSON、console 对 console 编码；caller 语义成对开启；zap 配置为官方推荐形态（生产配置 = JSON 编码 + ISO8601 时间）

> 完整原始数据见 [benchmark/bench_results.txt](../benchmark/bench_results.txt)，基准代码见 [benchmark/bench_test.go](../benchmark/bench_test.go)。

## 一、总览

| 场景 | go-logger v2 | zap v1.28 | 差距 |
|---|---|---|---|
| JSON · 纯消息 · 无 caller | **156 ns / 0 alloc** | 367 ns / 0 alloc | **快 2.3×** |
| JSON · 纯消息 · 带 caller | **627 ns / 1 alloc** | 1091 ns / 2 allocs | **快 1.7×** |
| JSON · 10 字段（强类型） | **660 ns / 1 alloc** | 1117 ns / 1 alloc | **快 1.7×** |
| JSON · 10 字段（kv 键值对） | **980 ns / 4 allocs** | 1611 ns / 2 allocs（Sugar） | **快 1.6×** |
| JSON · 并行纯消息 | **25 ns / 0 alloc** | 43 ns / 0 alloc | **快 1.7×** |
| JSON · 并行 10 字段 | **308 ns / 1 alloc** | 420 ns / 1 alloc | **快 1.4×** |
| Console · 纯消息 | **65 ns / 0 alloc** | 421 ns / 3 allocs | **快 6.5×** |
| Console · 纯消息（Sugar） | 65 ns / 0 alloc | 1307 ns / 6 allocs | **快 20×** |
| 文件直写（JSON） | **2162 ns** | 2311 ns | 略快 6% |
| 文件缓冲写（64KB 缓冲） | **216 ns** | —（zap 无内建缓冲 Sink） | **快 10×**（vs 直写） |
| 级别过滤（error 阈值） | 5.7 ns | 4.1 ns | 同数量级，zap 略快 |
| 参照：标准库 log | 2.8 ns（无时间/级别/结构化） | — | 仅作吞吐下限参照 |

## 二、单线程基准明细

### 2.1 JSON 编码 · 纯消息（最基础热路径）

```
BenchmarkJSON_MsgOnly_GoLogger-20         156.2 ns/op    0 B/op    0 allocs/op
BenchmarkJSON_MsgOnly_Zap-20              366.5 ns/op    0 B/op    0 allocs/op
```

双方均为 0 分配。go-logger 更快的原因：

1. **时间编码**：手工逐字段写 "2006-01-02 15:04:05.000"（除法+查表），zap ISO8601 走其专用编码器（tz 偏移处理更复杂）；
2. **编码器路径更短**：本库 Encoder 直写池化 Buffer；zap 的 jsonEncoder 有可复用缓冲 + reflectBuf 旁路（ReflectedEncoder 惰性初始化检查）；
3. **无 CheckedEntry 二次分发**：本库 Logger→Core→Sink 单跳；zap Logger→Check（池化 CheckedEntry 组装）→ioCore→Encoder→WriteSyncer 多层。

### 2.2 JSON 编码 · 带 caller

```
BenchmarkJSON_MsgOnlyCaller_GoLogger-20   627.3 ns/op  240 B/op  1 allocs/op
BenchmarkJSON_MsgOnlyCaller_Zap-20       1091.2 ns/op  265 B/op  2 allocs/op
```

caller 采集（`runtime.Callers` + `CallersFrames`）是双方共同的大头开销（本库 +471ns，zap +725ns），1 次 240B 分配来自 `CallersFrames` 迭代器，属 runtime 固有成本。本库将 file/line 拆分存储、编码期直接追加，省去了 "file:line" 字符串拼接与一次接口装箱。

### 2.3 JSON 编码 · 10 个结构化字段

```
BenchmarkJSON_10FieldsTyped_GoLogger-20   659.7 ns/op  705 B/op  1 allocs/op   （强类型 Field）
BenchmarkJSON_10Fields_Zap-20            1117.4 ns/op  705 B/op  1 allocs/op   （zap.Field）
BenchmarkJSON_10FieldsKv_GoLogger-20      980.4 ns/op  765 B/op  4 allocs/op   （Infow 键值对）
BenchmarkJSON_10FieldsKv_ZapSugar-20     1610.8 ns/op 1435 B/op  2 allocs/op   （Sugar.Infow）
```

- 强类型路径双方同为 1 次分配（字段切片本身）；本库快 1.7×，得益于字段位模式直存（float64 IEEE754 位模式 / time UnixNano 存入 Int64，无 interface{} 装箱）与更精简的编码分支；
- kv 键值对路径本库 4 allocs 但字节数减半（765B vs 1435B）：`kvToFields` 构造 Field 切片，未命中强类型分支的字段走 Any 兜底；总体仍快 1.6× 且内存友好。

### 2.4 Console 编码

```
BenchmarkConsole_MsgOnly_GoLogger-20       64.8 ns/op    0 B/op  0 allocs/op
BenchmarkConsole_MsgOnly_Zap-20           421.1 ns/op   64 B/op  3 allocs/op
BenchmarkConsole_MsgOnly_ZapSugar-20     1306.8 ns/op  346 B/op  6 allocs/op
```

差距最大的一组。zap 的 console 编码器内部用**池化的 sliceArrayEncoder + `fmt.Fprint`** 逐元素输出元数据（源码注释也承认此路径非零分配），Sugar 更是每参数一次反射。本库 console 编码器全程手工追加（级别着色用零分配的 `AppendTo`），3 次分配降为 0。

## 三、并行基准明细（多核扩展性）

```
BenchmarkJSON_MsgOnly_Parallel_GoLogger-20     24.7 ns/op  0 allocs
BenchmarkJSON_MsgOnly_Parallel_Zap-20          43.1 ns/op  0 allocs
BenchmarkJSON_10Fields_Parallel_GoLogger-20   308.4 ns/op  1 alloc
BenchmarkJSON_10Fields_Parallel_Zap-20        419.5 ns/op  1 alloc
```

并行模式下差距收窄（1.4~1.7×），双方在多核下的竞争点都是 `sync.Pool` 与原子操作。本库并行纯消息 24.7ns/op ≈ 4000 万条/秒的纯编码吞吐上限。

## 四、文件写入（含滚动切割）

```
BenchmarkFile_GoLogger_Rotator-20         2162 ns/op   0 allocs/op   （go-logger 滚动切割文件）
BenchmarkFile_Zap_OsFile-20               2311 ns/op   0 allocs/op   （zap 直写 os.File）
BenchmarkFile_Zap_ViaRotator-20           2308 ns/op   0 allocs/op   （zap 经本库 Rotator 写）
BenchmarkFile_GoLogger_RotatorBuffered-20  216 ns/op   0 allocs/op   （go-logger + 64KB 缓冲写）
```

- **滚动切割器零开销**：zap 直写 os.File 与 zap 经本库 Rotator 写几乎同速（2311 vs 2308ns），证明 Rotator 的每次写入检查（一次整数比较 + 互斥锁）在文件系统调用面前完全不可见；
- **go-logger 整体略快 6%**：来自编码层优势（2.1 节）；
- **缓冲写是吞吐关键**：64KB 聚合缓冲 + 后台定时刷盘将每条开销从 2162ns 降到 216ns（**10×**），代价是崩溃时最多丢一个刷盘周期内的日志。生产建议 `FileBufferSizeKB=64` + `FlushIntervalSec=30`。

## 五、级别过滤

```
BenchmarkFilteredOut_GoLogger-20   5.66 ns/op  0 allocs   （error 阈值 + Info 调用）
BenchmarkFilteredOut_Zap-20        4.07 ns/op  0 allocs
```

双方都是"一次原子读"级别的过滤。**被禁用的日志不产生任何分配与 I/O**，这一 zap 的核心卖点在本库完整保留。

本库稳定慢约 1.5ns，原因经归因微基准实测（[filter_bench_test.go](../filter_bench_test.go)，同机复测 5.34 vs 3.86 与归档值一致）确认是 **API 形态差异，过滤算法本身与 zap 一致**：

| 归因分段 | 实测（5 轮均值） | zap 对照 |
|---|---|---|
| ① legacy API 消息预处理 `formatMessage`：签名 `(f interface{}, v ...interface{})` 决定消息必须在级别检查**之前**先转成 string（一次接口断言） | ~1.4 ns | 无——`Info(msg string)` 无预处理 |
| ② 包级 API 每次调用经 `std.Load()` 定位全局 logger | ~0.7 ns | 无——基准持局部 `*zap.Logger` 变量 |
| ③ 动态级别 `AtomicLevel.Enabled`（一次原子读）vs 基准中 zap 的静态 `zapcore.InfoLevel`（纯整数比较） | ~0.3~1.3 ns | 静态值 |

修正说明：原报告初稿称"本库经一层接口分发略多"——**不成立**。zap 的 `ioCore` 同样内嵌 `LevelEnabler`，双方接口分发深度完全相同（Core → 内嵌 LevelEnabler 两跳），已从结论中更正。

补充：

- 第 ③ 段属基准配置差异：zap 生产环境惯用 `zapcore.NewAtomicLevel`（同样一次原子读），届时此项差距消失；
- 同进程加和交叉验证：实例方法路径（无 ①②）2.0 ns + ①1.4 + ②0.7 ≈ 4.2 ns ≈ 完整路径 4.3 ns ✓；
- 消除方式：把 `formatMessage` 移到级别检查之后即可省掉 ①，代价是启用热路径多一次 Enabled 检查或拆分 log() 调用链（并需重校准 caller 帧偏移）——纳秒级收益不值得动已验证的热路径，故未采纳。

## 六、结论

1. **性能目标达成**：在零第三方依赖的前提下，go-logger v2 在所有编码类场景全面快于 zap（1.4~6.5×），文件写入持平略优，级别过滤同数量级；
2. **内存行为**：热路径（纯消息/console/过滤）0 分配；结构化路径与 zap 同为 1 次分配（字段切片）；
3. **超越 zap 的点**：console 编码（6.5×）、kv 键值对的内存开销（减半）、内建缓冲写 Sink（10×）、滚动切割器开箱即用（zap 需额外引入 lumberjack）；
4. **略逊 zap 的点**：级别过滤慢 ~1.5ns（legacy API 消息预处理与全局定位所致，逐项归因见第五节）；kv 路径多 2 次小分配。均为纳秒级差异，不影响选择；
5. **推荐生产配置**：`Encoding=json` + `FileBufferSizeKB=64` + `Sampling`（高频重复场景），编码开销占比进一步摊薄。

## 七、复现方式

```bash
cd go-logger/benchmark
go test -bench=. -benchmem -count=5 -benchtime=1s > bench_results.txt
```

注意：
- 关闭其他高负载程序，笔记本接电源运行（避免降频）；
- 首轮结果波动较大，`-count=5` 取均值，必要时用 benchstat 做统计检验；
- Windows 与 Linux 的文件系统调用开销差异明显，文件基准结论在其他平台需重测。

---
*原始数据归档于 benchmark/bench_results.txt（含 5 轮完整明细）。*
