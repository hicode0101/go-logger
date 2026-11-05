# go-logger v2 架构设计与 zap 源码分析

> 本文回答两个问题：**zap 为什么快**（源码级分析），以及 **go-logger 如何在零第三方依赖的前提下实现同等能力**。

## 一、zap 源码实现分析

### 1.1 zap 的性能根源

对 zap v1.28.0 源码的逐文件分析，其高性能来自以下六项核心技术：

| 技术 | 源码位置 | 作用 |
|---|---|---|
| 缓冲池 + []byte 直写 | `buffer/pool.go`、`internal/pool` | 每条日志编码复用 `sync.Pool` 中的 1KiB `[]byte`，消除 string 拼接与中间对象分配 |
| 类型化字段（Field） | `zapcore/field.go` | `Field{Key, Type, Int64, Interface}` 以类型枚举 + 强类型构造函数（`zap.Int/String/...`）绕开反射；`zap.Any` 才走反射兜底 |
| strconv 零分配格式化 | `buffer/buffer.go` | `AppendInt/AppendFloat/AppendTime` 直接调用 `strconv.Append*` 追加到切片，不经过 `fmt`（fmt 走反射且有分配） |
| 级别原子过滤 + check 早退 | `logger.go check()`、`zapcore/level.go AtomicLevel` | 禁用级别的日志在构造任何对象之前就返回：一次 `atomic.Int32` 读完成过滤 |
| Caller/堆栈的池化采集 | `internal/stacktrace/stack.go` | 只取 1 个 PC 定位 caller；堆栈捕获用可扩容的 `[]uintptr` 池，Error 级才触发 |
| 采样器无锁计数 | `zapcore/sampler.go` | 按"级别 × fnv32a(消息) 哈希槽"组织原子计数器数组，同一消息前 N 条全量、之后每 M 条取 1，全程无互斥锁 |

### 1.2 关键实现细节

**编码器体系**：`zapcore.Encoder` 接口 + JSON/Console 两种实现。Console 编码器内部**复用 JSON 编码器**编码字段块（`consoleEncoder{ *jsonEncoder }`），元数据部分用池化的 `sliceArrayEncoder` + `fmt.Fprint` 输出——即 zap 的 console 路径本身仍有少量 `fmt` 开销。

**时间编码**：zap 对每种 layout 有专用编码器（`zapcore/time.go`），手工写数字而非 `time.Format`。`time.Format` 每次需解析 layout 模板（约 100-200ns 且分配），手工编码只需几次整数除法。

**Core 管线**：`Logger.check()` → `Core.Check(entry, ce)` 把启用的核心追加进池化的 `*CheckedEntry` → `ce.Write(fields)` 逐核心写出并处理 Panic/Fatal 副作用。`tee`（多路）在 Check 阶段逐子核心独立过滤级别，实现"控制台 info / 文件 error"这类差异化输出。

**Sink 注册表**：`zapcore/sink.go` 按 URL scheme 注册工厂（`file://`、`http://` 等），`zap.Open()` 解析配置 URL 后构造 Sink——这是 zap 的输出端扩展点。

**缓冲写**：`zapcore/buffered_write_syncer.go` 用 `bufio.Writer` 包装 Sink + 后台定时 Flush 协程，批量小写入降低系统调用次数。

### 1.3 zap 的依赖清单

zap 自身除标准库外仅依赖 `go.uber.org/multierr`（错误聚合）。它的"重"不在于依赖而在于**通用性**：反射编码路径、ObjectMarshaler/ArrayMarshaler 扩展面、`zapgrpc`/`zapio`/`zaptest` 子包、大量防御性 API。对只需要"高性能文件/控制台日志"的场景，这些是可裁剪的。

## 二、go-logger v2 的对应实现

### 2.1 总体架构

```
业务代码
   │  Info/Infof/Infow + Field（强类型构造函数）
   ▼
Logger ─── 级别原子过滤（AtomicLevel，禁用即返回，0 alloc）
   │  构造 Entry（池化对象，含 Time/Caller/Stack，按需采集）
   ▼
Core（处理链接口，可 tee / 采样 / hook 装饰）
   │  ioCore：Encoder 编码到池化 Buffer → Sink.Write
   ▼
Sink（字节流目的地接口：本地文件 / 控制台 / 自定义注册）
   │
   ├── consoleSink   → os.Stdout（行级锁）
   └── fileSink      → Rotator（lumberjack 等价的滚动切割）
                          └── bufferedSink（可选缓冲写 + 后台定时刷盘）
```

三个扩展接口与 zap 的对应关系：

| go-logger 接口 | zap 对应 | 扩展用途 |
|---|---|---|
| `Encoder`（Encode 到池化 Buffer） | `zapcore.Encoder` | 自定义输出格式（logfmt/CSV/Protobuf…） |
| `Core`（Enabled/Write/Sync/Close/With） | `zapcore.Core`（+Close） | 结构化远程目的地（ES 文档、StarRocks 行批） |
| `Sink`（io.Writer+Syncer+Closer） | `zapcore.WriteSyncer` + sink registry | 字节流远程目的地（Kafka、MinIO、socket） |

### 2.2 热路径设计（每条日志的执行序）

```go
func (log *Logger) log(lvl Level, msg string, fields []Field) {
    // 1. 原子级别过滤：一次 atomic.Int32 读，禁用级别 0 分配返回
    if !log.core.Enabled(lvl) { return }
    // 2. 池化 Entry：Get + 整体重置，消除接口调用导致的逃逸堆分配
    entry := entryPool.Get().(*Entry)
    *entry = Entry{Level: lvl, Time: time.Now(), ...}
    // 3. 按需采集：caller（1 个 PC）、堆栈（仅 Error+，池化 []uintptr）
    // 4. 核心写出：编码直写池化 Buffer → Sink.Write
    err := log.core.Write(entry, fields)
    // 5. 回收条目（约定：Core/Hook 不得跨调用持有 *Entry）
    entryPool.Put(entry)
    // 6. Panic/Fatal 副作用
}
```

### 2.3 zap 各项技术在本库的落点

| zap 技术 | go-logger 实现位置 | 说明 |
|---|---|---|
| buffer.Pool（1KiB 池） | `buffer.go` `bufferPool/getBuffer/putBuffer` | 超过 64KiB 的膨胀缓冲不入池，防内存驻留 |
| 手工时间编码 | `buffer.go` `appendTimeLayout` + 两位数字查表 | "2006-01-02 15:04:05.000" 逐字段手写，毫秒三位数编码 |
| JSON 转义 | `encoding.go` `appendJSONString` | 快路径整段透传安全字符；控制字符/非法 UTF-8 逐字节处理，与 encoding/json 语义对齐 |
| 类型化 Field | `field.go` | 比 zap 更进一步：**float64 用 IEEE754 位模式、time 用 UnixNano 存入 Int**，彻底消除 interface{} 装箱（zap 的 float/time 字段仍有装箱） |
| AtomicLevel | `level.go` | `atomic.Int32` 存储；多核心共享同一实例实现 SetLevel 全局生效 |
| check 早退 | `logger.go log()` | 顺序：级别过滤 → Entry 池化 → 按需 caller/堆栈 |
| 采样器 | `core.go` `samplerCore` | 移植 zap 计数表算法：级别×64 槽原子计数，1 分钟窗口 CAS 重置 |
| console 编码器复用 JSON | `encoder.go` | 同样复用：字段块经 `appendFieldsJSON` 编码；元数据手工追加（比 zap 的 `fmt.Fprint` 路径更快） |
| sink 注册表 | `sink.go` `RegisterSink` | 按类型名注册工厂（console/file 内置），扩展方传 `Options map` 注入连接参数 |
| buffered write syncer | `writer.go` `writerBuf` + `sink.go` `bufferedSink` | 手写聚合缓冲 + 后台定时刷盘；bufio 满即刷语义一致 |
| lumberjack 滚动切割 | `rotate.go` `Rotator` | 见 2.4 |
| Caller 最小采集 | `caller.go` | `runtime.Callers(skip+2, pcs[:1])` 单 PC + CallersFrames |
| 池化堆栈 | `caller.go` `stackPool` | 64 帧 `[]uintptr` 池，截断上限防递归失控 |

### 2.4 滚动切割器（lumberjack 等价实现）

`rotate.go` 复刻 lumberjack v2.2.1 的完整行为：

- **按大小切割**：`Write` 前 `size+len > maxBytes` 则 rotate——关闭当前文件 → `os.Rename` 为 `前缀-2006-01-02T15-04-05.000.扩展名` → 打开新文件（`O_APPEND|O_CREATE`，保留权限位）；
- **按个数清理**：历史文件按文件名时间戳从新到旧排序，超出 `MaxBackups` 删除；
- **按天数清理**：文件名时间戳早于保留窗口的删除（与 lumberjack 一致：按文件名而非 mtime）；
- **gzip 压缩**：压缩成功才删除原文，失败保留；压缩在后台 mill 协程执行；
- **后台 mill 协程**：`millCh` 缓冲为 1 非阻塞投递，请求去重，不阻塞写入路径；
- **惰性重建**：文件不存在/被移走时自动重开；
- **单条超限**：超过 `MaxSizeMB` 的单条写入返回错误（对齐 lumberjack）。

### 2.5 依赖对比

| 项 | 旧版 go-logger | zap v1.28 | 新版 go-logger v2 |
|---|---|---|---|
| 直接依赖 | zap + lumberjack + multierr | multierr | **0** |
| 编译进二进制 | ~15 包 | ~20 包 | 本包 12 文件 |
| 反射使用 | zap Any/Console 路径 | Any/reflected 路径 | 仅 Any 字段兜底 |

### 2.6 已知取舍

- **Entry 生命周期**：`*Entry` 池化复用，Core/Hook 实现不得跨调用持有引用（zap CheckedEntry 相同契约）。异步 Sink 必须先拷贝所需数据。
- **堆栈深度上限 64 帧**：深层递归截断（zap 可无限扩容）；换取采集成本有界。
- **采样窗口固定 1 分钟**：与 zap 默认一致，不可配置。
- **mill 协程常驻**：与 lumberjack 一致，每个 Rotator 一个清理协程。
- **兼容 API 语义变化**：`Named/WithFields/GetLogger` 返回 `*logger.Logger`（原为 `*zap.Logger`）；`TimeEncoder/ColorLevelEncoder` zapcore 函数由内置编码器选项替代。
