# go-logger v2 接口说明与扩展指南

go-logger v2 的输出端完全可插拔。内置实现了 **本地文件**（含滚动切割/缓冲写/压缩清理）与 **控制台**；接入 Elasticsearch、Kafka、StarRocks、MinIO 等远程目的地不需要改动库本身——实现本文所述接口并注册即可。

## 一、扩展接口总览

```
业务代码 → Logger（过滤/编码调度）
              │
              ├── Core（结构化事件处理链）── 推荐给"按文档/行批量入库"的目的地
              │      └── Sink（字节流目的地）── 推荐给"按字节块发送"的目的地
              └── Encoder（格式化）── 推荐给需要自定义输出格式的场景
```

### 1.1 Sink 接口（字节流目的地）

```go
// Sink 日志字节流的最终目的地。
type Sink interface {
    io.Writer          // 写出编码后的日志字节（含行结束符，整行原子写入）
    io.Closer          // 释放连接/句柄；实现应先刷写缓冲
    Sync() error       // 强制刷写内部缓冲；不支持则为空操作
}

// SinkFactory 按配置创建 Sink；注册后即可在配置中按类型名引用。
type SinkFactory func(cfg SinkConfig) (Sink, error)

func RegisterSink(typ string, factory SinkFactory)   // 重复注册覆盖
```

`SinkConfig` 关键字段：

| 字段 | 说明 |
|---|---|
| `Type` | 注册的类型名（`console`/`file`/自定义） |
| `Format` | 编码器名（`console`/`json`/自定义），决定字节流的格式 |
| `Level` | 本路输出独立级别（空 = 跟随全局） |
| `BufferSizeKB` / `FlushIntervalSec` | >0 时启用缓冲写 + 后台定时刷盘（对网络目的地尤其有用） |
| `Options` | 自定义键值参数（brokers、地址、bucket…），由你的工厂自行解析 |

**适用场景**：目的地按"字节块"消费——Kafka（produce 字节消息）、MinIO（追加对象）、HTTP 网关等。库内 `ioCore` 已替你完成级别过滤、编码、缓冲，你只需要实现"把字节可靠地送到远方"。

**必须遵守的契约**：

1. `Write` 必须并发安全（库内多个核心可能共享同一 Sink，或你自行用 `NewLockedSink` 包装）；
2. `Close` 前必须刷写缓冲，防止丢日志；
3. 长期网络写入建议内部做批量 + 异步发送，`Write` 只入队不阻塞；
4. 发送失败时优先降级（丢弃 + 计数）而非阻塞调用方。

### 1.2 Core 接口（结构化事件处理链）

```go
// Core 一条日志事件的处理链节点。
type Core interface {
    Enabled(lv Level) bool                       // 必须廉价：单次原子读或布尔判断
    Write(entry *Entry, fields []Field) error    // entry 只读且**不得跨调用持有**
    Sync() error
    Close() error
    With(fields []Field) Core                    // 返回携带固定字段的克隆
}

func NewCore(level LevelEnabler, enc Encoder, sink Sink) Core  // 通用 ioCore
func NewTee(cores ...Core) Core                                // 多路
func AddHook(core Core, hooks ...Hook) Core                    // 事件回调装饰
```

**适用场景**：目的地按"结构化文档"消费——Elasticsearch（_bulk 文档）、StarRocks（Stream Load 行批）、ClickHouse（batch insert）等。实现 `Core` 可直接拿到 `Entry + []Field` 结构化数据，批量转换后入库，跳过文本编码步骤。

**契约**：

1. `Enabled` 在每条日志上调用，必须是无锁/单次原子操作；
2. `Write` 的 `*Entry` 在返回后立即被回收复用（池化），**禁止保存引用**；异步批量入库需先把 entry/fields 转换成你自己的结构再入队；
3. `With` 应返回克隆且共享底层状态（参考 `ioCore.With`）。

### 1.3 Encoder 接口（输出格式）

```go
type Encoder interface {
    Encode(buf *Buffer, entry *Entry, fields []Field)  // 编码到池化 Buffer，末尾必须补 "\n"
    Clone() Encoder
}

func RegisterEncoder(name string, factory func(EncoderConfig) Encoder)
```

**适用场景**：需要非文本格式（Protobuf 消息、CSV 表头对齐、logfmt）或不同文本布局。注册后在 `Config.Encoding` / `SinkConfig.Format` 中引用。

## 二、注册与配置接入

实现方注册工厂（建议放在包 `init()` 中）：

```go
func init() {
    logger.RegisterSink("kafka", newKafkaSink)
}

func newKafkaSink(cfg logger.SinkConfig) (logger.Sink, error) {
    brokers := strings.Split(cfg.Options["brokers"], ",")
    topic := cfg.Options["topic"]
    // ... 创建 producer
}
```

使用方在配置中引用：

```go
_ = logger.Init(logger.Config{
    Level: "info",
    Sinks: []logger.SinkConfig{
        {Type: "file",  Format: "json", Path: "logs/app.log", MaxSizeMB: 100},
        {Type: "kafka", Format: "json", BufferSizeKB: 64,
            Options: map[string]string{
                "brokers": "10.0.0.1:9092,10.0.0.2:9092",
                "topic":   "app-logs",
            }},
    },
})
```

`Options` 键值对由实现方自定义，不进入 go-logger 核心配置结构，保证核心零依赖、扩展零侵入。

## 三、四个远程目的地的实现骨架

以下骨架展示每类目的地的关键决策点，生产化时按需补齐重试、鉴权与监控。
Kafka 与 Elasticsearch 两类已提供**可直接运行的完整示例**（独立模块，客户端 SDK 不进入主模块依赖树）：

| 目的地 | 示例 | 扩展模式 |
|---|---|---|
| Kafka | [example/kafka](../example/kafka)（`go run ./kafka`） | 字节流 Sink + kafka-go |
| Elasticsearch | [example/elasticsearch](../example/elasticsearch)（`go run ./elasticsearch`） | 结构化 Core + `logger.NewLogger` + net/http `_bulk` |

### 3.1 Kafka（字节流 Sink，批量异步发送）

```go
type kafkaSink struct {
    queue   chan []byte      // 有界队列：满则丢弃计数，绝不阻塞调用方
    done    chan struct{}
    dropped atomic.Uint64
    // producer sarama/confluent-kafka-go 等由扩展方自行引入
}

func (k *kafkaSink) Write(p []byte) (int, error) {
    b := make([]byte, len(p))          // 拷贝：p 由调用方复用，不可持有
    copy(b, p)
    select {
    case k.queue <- b:                 // 非阻塞入队
    default:
        k.dropped.Add(1)               // 队列满：丢弃 + 计数，保护业务
    }
    return len(p), nil
}

// 后台协程：攒批（如 500 条 / 200ms）→ producer.SendMessages
// Sync():   等待队列清空；Close(): 刷批 → 关闭 producer
```

要点：**拷贝再入队**（调用方缓冲复用）、**有界队列非阻塞**、**Sync/Close 保证 flush**。

### 3.2 Elasticsearch（结构化 Core，_bulk 批量索引）

接入点：实现本接口后经 **`logger.NewLogger(core)`** 包装成日志器实例（实例方法与全局 logger 完全一致）；
克隆共享发送管线时注意不要复制锁/原子字段（示例用共享 `esPipeline` 结构解决）。

```go
type esCore struct {
    core     logger.Core          // 可保留一路内部 ioCore 用于本地降级
    queue    chan esDoc
}

func (e *esCore) Enabled(lv logger.Level) bool { return e.core.Enabled(lv) }

func (e *esCore) Write(entry *logger.Entry, fields []logger.Field) error {
    doc := map[string]interface{}{
        "level":   entry.Level.String(),       // 必须在 Write 内完成转换
        "time":    entry.Time.Format(...),
        "message": entry.Message,
    }
    for _, f := range fields {                 // 结构化字段 → 文档字段
        doc[f.Key] = fieldValue(f)             // 按 FieldType 解出强类型值
    }
    select { case e.queue <- doc: default: /* 丢弃计数 */ }
    return nil
}

// 后台协程：攒 1000 条 / 5s → POST /_bulk（NDJSON：action + doc 交替行）
// 失败处理：429/5xx 退避重试；本地 file sink 降级兜底
```

要点：**Write 内同步转换**（Entry 池化不可持有）、**_bulk NDJSON 格式**、**本地文件双写兜底**。

### 3.3 StarRocks（结构化 Core，Stream Load 行批）

```go
// Write 内把 entry+fields 转成一行 CSV/JSON 行：
//   "level\ttime\tlogger\tcaller\tmsg\tfield1\tfield2\n"
// 后台协程：攒批（10MB / 10s）→
//   curl -u user:pass -H "label: app-log-20240601-0001" \
//        -H "format: csv" -H "column_separator: \t" \
//        --data-binary @batch \
//        http://fe-host:8030/api/db/log_table/_stream_load
// label 唯一保证幂等；失败退避重试或降级本地文件
```

要点：**label 幂等重试**、**攒批大小控制**（Stream Load 单次建议 ≤10MB）、**列与建表对齐**。

### 3.4 MinIO（字节流 Sink，分对象滚动上传）

```go
type minioSink struct {
    mu      sync.Mutex
    client  *minio.Client
    bucket  string
    objName string                 // logs/2024-06-01/app-0001.log
    buf     *logger.Buffer         // 攒块（如 8MB 或 5 分钟滚动一个对象）
}

func (m *minioSink) Write(p []byte) (int, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    m.buf.Write(p)                              // 本地先聚合
    if m.buf.Len() >= chunkSize { m.rotate() }  // 达到阈值：上传旧对象，开新对象
    return len(p), nil
}

// rotate(): PutObject(bucket, objName, ...)  →  objName 序号自增
// Close():  上传剩余块
```

要点：**按大小/时间滚动对象名**、**本地位写双兜底**（MinIO 不可用时落本地）、**复用 Buffer 避免重复分配**。

### 3.5 通用建议

- **每路独立级别**：`SinkConfig.Level = "error"` 可让远程目的地只收 error，降低成本；
- **降级链**：远程不可用 → 落本地文件 → 恢复后补传（可在扩展内实现重放队列）；
- **Sync 语义**：优雅停机时 `logger.Sync()` → 各 Sink 刷批；`logger.Close()` → 释放连接；
- **不回压业务**：任何目的地故障都不应阻塞业务线程——入队非阻塞 + 丢弃计数是底线。

## 四、扩展自定义编码器示例

```go
func init() {
    logger.RegisterEncoder("logfmt", func(c logger.EncoderConfig) logger.Encoder {
        return logfmtEncoder{}
    })
}

type logfmtEncoder struct{}

func (logfmtEncoder) Encode(buf *logger.Buffer, entry *logger.Entry, fields []logger.Field) {
    buf.Reset()
    buf.AppendTime(entry.Time, logger.TimeLayout())   // 快路径时间编码
    buf.AppendByte(' ')
    buf.AppendString("level=")
    buf.AppendString(entry.Level.String())
    buf.AppendString(" msg=")
    buf.AppendQuote(entry.Message)
    for _, f := range fields {
        buf.AppendByte(' ')
        appendLogfmtField(buf, f)
    }
    buf.AppendByte('\n')
}

func (logfmtEncoder) Clone() logger.Encoder { return logfmtEncoder{} }
```

## 五、内置实现参考

| 文件 | 内容 |
|---|---|
| `sink.go` | Sink 接口、注册表、console/file 内置工厂、bufferedSink、lockedSink |
| `core.go` | Core 接口、ioCore、teeCore、samplerCore、hooksCore、nopCore |
| `encoder.go` | Encoder 接口、console/json 实现、注册表 |
| `rotate.go` | Rotator 滚动切割文件写入器（Sink 实现） |
| `writer.go` | writerBuf 聚合缓冲（bufio 的 Sink 定制版） |

实现新 Sink/Core 前建议通读这几个文件——内置实现就是扩展的参考样板。
