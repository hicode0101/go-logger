package logger

import (
	"io"
	"net/url"
	"os"
	"sync"
	"time"
)

// Sink 日志字节流的最终目的地（本地文件、控制台、socket、远程存储管道…）。
//
// 设计定位：Sink 只负责"字节到目的地"，不感知级别/格式；
// 结构化数据 → Core → 字节 → Sink 的分层使输出端与编码端完全解耦。
//
// 扩展方式（详见 docs/EXTEND.md）：
//   - 字节流型目的地（Kafka producer、HTTP 网关…）：实现 Sink，
//     内部自行批量缓冲 + 异步发送，RegisterSink 注册工厂后即可配置引用；
//   - 结构体型目的地（Elasticsearch 文档、StarRocks Stream Load…）：
//     推荐 直接实现 Core（拿到结构化 Entry+Field 批量转换）。
type Sink interface {
	io.Writer
	io.Closer
	// Sync 刷写内部缓冲；对不支持语义的目的地（如 stdout）为空操作。
	Sync() error
}

// SinkFactory Sink 工厂：按解析后的配置创建 Sink 实例。
type SinkFactory func(cfg SinkConfig) (Sink, error)

// sinkRegistry 按类型名注册的 Sink 工厂表。
var sinkRegistry = struct {
	mu sync.RWMutex
	m  map[string]SinkFactory
}{m: map[string]SinkFactory{}}

// RegisterSink 注册 Sink 工厂，重复注册覆盖。
// 注册后可在 Config.Sinks / SinkConfig.Type 中按名称引用。
// 内置注册：console、file。第三方示例注册名：kafka、elasticsearch、starrocks、minio。
func RegisterSink(typ string, factory SinkFactory) {
	sinkRegistry.mu.Lock()
	defer sinkRegistry.mu.Unlock()
	sinkRegistry.m[typ] = factory
}

// newSink 按配置构造 Sink；未知类型返回错误。
func newSink(cfg SinkConfig) (Sink, error) {
	sinkRegistry.mu.RLock()
	f, ok := sinkRegistry.m[cfg.Type]
	sinkRegistry.mu.RUnlock()
	if !ok {
		return nil, errUnknownSinkType(cfg.Type)
	}
	return f(cfg)
}

// SinkConfig 单个输出目标的配置。
// 内置 sink 只消费部分字段；自定义 sink 可用 Options 传递任意键值参数
// （broker 地址、索引名、bucket 等），保持核心配置结构稳定。
type SinkConfig struct {
	Type   string // sink 类型：console / file / RegisterSink 注册的自定义名
	Format string // 编码器名：console / json / RegisterEncoder 注册的自定义名
	Level  string // 独立级别（低于全局级别时不生效）；空表示跟随全局级别
	Color  bool   // 控制台类型 sink 是否着色（仅编码器为 console 时生效）

	// ---- file 类型专用（lumberjack 等价参数）----
	Path       string // 日志文件路径
	MaxSizeMB  int    // 单文件最大体积（MB），超过自动切割
	MaxBackups int    // 最多保留的历史文件个数（0=不限）
	MaxAgeDays int    // 历史文件最长保留天数（0=不限）
	Compress   bool   // 历史文件 gzip 压缩
	LocalTime  bool   // 切割文件名使用本地时间（false 为 UTC）

	// ---- 缓冲写（对 file / 自定义 sink 通用）----
	BufferSizeKB     int // >0 时启用缓冲写；0=直写（每条落盘，崩溃不丢）
	FlushIntervalSec int // 缓冲模式下后台刷盘周期（秒），0=默认 30s；仅 BufferSizeKB>0 时生效

	// ---- 自定义 sink 扩展参数（Kafka brokers、ES 地址、bucket…）----
	Options map[string]string
}

// errUnknownSinkType 构造未知 sink 类型错误。
func errUnknownSinkType(typ string) error {
	return &unknownSinkTypeError{typ: typ}
}

type unknownSinkTypeError struct{ typ string }

func (e *unknownSinkTypeError) Error() string {
	return "logger: 未知 sink 类型 " + e.typ + "（用 RegisterSink 注册后再引用）"
}

// ---------------------------------------------------------------------------
// 内置 sink 实现
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// console sink：os.Stdout（或 os.Stderr）
// ---------------------------------------------------------------------------

type stdoutSink struct {
	w  *os.File
	mu *sync.Mutex
}

func init() {
	RegisterSink("console", func(cfg SinkConfig) (Sink, error) {
		out := os.Stdout
		if cfg.Options["stream"] == "stderr" {
			out = os.Stderr
		}
		return &stdoutSink{w: out, mu: &sync.Mutex{}}, nil
	})
}

// Write 并发安全的整行写出（追加模式无需跨写持锁，但 stdout 可能被
// 多个 sink 共享，写粒度持锁避免行间穿插）。
func (s *stdoutSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Sync 空（unbuffered stdout 无缓冲可刷）。
func (s *stdoutSink) Sync() error { return nil }

// Close stdout 由进程持有，不允许关闭。
func (s *stdoutSink) Close() error { return nil }

// ---------------------------------------------------------------------------
// file sink：内置滚动切割文件（lumberjack 等价实现）
// ---------------------------------------------------------------------------

func init() {
	RegisterSink("file", func(cfg SinkConfig) (Sink, error) {
		rot := &Rotator{
			Filename:   cfg.Path,
			MaxSizeMB:  cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
			MaxAgeDays: cfg.MaxAgeDays,
			Compress:   cfg.Compress,
			LocalTime:  cfg.LocalTime,
		}
		var s Sink = rot
		// 可选缓冲写：小消息高频写入场景显著提升吞吐，代价是
		// 崩溃丢失缓冲内日志；FlushInterval 后台定时兜底刷盘。
		if cfg.BufferSizeKB > 0 {
			interval := 30
			if cfg.FlushIntervalSec > 0 {
				interval = cfg.FlushIntervalSec
			}
			s = newBufferedSink(rot, cfg.BufferSizeKB*1024, interval)
		}
		return s, nil
	})
}

// ---------------------------------------------------------------------------
// 通用包装
// ---------------------------------------------------------------------------

// bufferedSink 缓冲写包装：聚合小写入，降低系统调用次数。
// bufio.Writer 满或后台定时器触发时刷盘；Close/Sync 强制刷盘。
type bufferedSink struct {
	sink     Sink
	buf      *writerBuf
	interval FlushInterval
	stop     chan struct{}
	stopOnce sync.Once
}

// FlushInterval 后台刷盘周期（秒），0 表示禁用后台刷盘（仅满/手动刷）。
type FlushInterval int

// newBufferedSink 创建缓冲 sink 并启动后台刷盘协程（interval>0 时）。
func newBufferedSink(sink Sink, sizeBytes int, intervalSec int) Sink {
	bs := &bufferedSink{
		sink: sink,
		buf:  newWriterBuf(sink, sizeBytes),
		stop: make(chan struct{}),
	}
	if intervalSec > 0 {
		bs.interval = FlushInterval(intervalSec)
		go bs.backgroundFlush()
	}
	return bs
}

// Write 写入缓冲区，缓冲满时自动刷盘。
func (b *bufferedSink) Write(p []byte) (int, error) {
	n, err := b.buf.Write(p)
	return n, err
}

// Sync 刷写缓冲并透传给底层 sink。
func (b *bufferedSink) Sync() error {
	if err := b.buf.Flush(); err != nil {
		return err
	}
	return b.sink.Sync()
}

// Close 刷写缓冲、停止后台刷盘、关闭底层 sink。
func (b *bufferedSink) Close() error {
	b.stopOnce.Do(func() { close(b.stop) })
	if err := b.buf.Flush(); err != nil {
		_ = b.sink.Close()
		return err
	}
	return b.sink.Close()
}

// backgroundFlush 后台定时刷盘兜底：进程崩溃最多丢一个周期内的日志。
func (b *bufferedSink) backgroundFlush() {
	ticker := newTicker(int(b.interval))
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C():
			_ = b.buf.Flush()
		case <-b.stop:
			return
		}
	}
}

// newTicker 延迟构造 time.Ticker，便于测试替换。
func newTicker(seconds int) *timeTicker {
	return &timeTicker{t: time.NewTicker(time.Duration(seconds) * time.Second)}
}

// timeTicker time.Ticker 的薄包装，隔离测试依赖。
type timeTicker struct{ t *time.Ticker }

func (tt *timeTicker) C() <-chan time.Time { return tt.t.C }
func (tt *timeTicker) Stop()               { tt.t.Stop() }

// lockedSink 互斥锁包装：给无并发安全保证的 Sink 提供写保护。
type lockedSink struct {
	mu sync.Mutex
	s  Sink
}

// NewLockedSink 用互斥锁包装 s，保证并发 Write/Sync/Close 互斥。
func NewLockedSink(s Sink) Sink { return &lockedSink{s: s} }

func (l *lockedSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Write(p)
}

func (l *lockedSink) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Sync()
}

func (l *lockedSink) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Close()
}

// ---------------------------------------------------------------------------
// discardSink：丢弃全部写入（benchmark / 静音场景）
// ---------------------------------------------------------------------------

type discardSink struct{}

func (discardSink) Write(p []byte) (int, error) { return len(p), nil }
func (discardSink) Sync() error                 { return nil }
func (discardSink) Close() error                { return nil }

// NewDiscardSink 返回丢弃所有写入的 Sink，用于测试与基准。
func NewDiscardSink() Sink { return discardSink{} }

// parseSinkURL 解析形如 "file:///var/log/app.log?maxsize=100" 的 URL，
// 供扩展方实现 URL 驱动配置（可选工具函数）。
func parseSinkURL(raw string) (*url.URL, url.Values, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	return u, u.Query(), nil
}
