package logger

import (
	"sync/atomic"
	"time"
)

// Core 日志核心：一条日志事件从 Logger 到最终目的地的完整处理链节点。
//
// 扩展模型（与 zapcore.Core 对齐并增加 Close）：
//
//	Logger.check ──▶ Core.Enabled ──▶ Core.Write ──▶ Sink
//	（级别过滤）      （本节点是否启用）  （编码/写出）   （字节目的地）
//
// 实现约定：
//   - Enabled 必须是廉价调用（单次原子读或布尔判断），Logger 每条日志都会先调它；
//   - Write 收到的 entry/fields 只读，实现方不得修改后原样返回给上层；
//   - 接入 Elasticsearch / Kafka / StarRocks / MinIO 等远程存储时推荐实现本接口，
//     直接消费结构化 Entry+Field 做批量异步发送，见 docs/EXTEND.md。
type Core interface {
	// Enabled 报告 lv 级别的日志是否会被本核心处理。
	Enabled(lv Level) bool
	// Write 写出一条日志事件；返回的错误会由 Logger 上报到 stderr。
	// entry 以指针传递，避免大结构体经接口调用时的装箱堆分配。
	Write(entry *Entry, fields []Field) error
	// Sync 刷写内部缓冲（如有）。
	Sync() error
	// Close 释放底层资源（文件句柄、网络连接），之后本核心不可再使用。
	Close() error
	// With 返回携带固定字段的克隆核心；实现应共享底层状态、只复制必要切片。
	With(fields []Field) Core
}

// ioCore 级别过滤 + 编码 + 写字节流的通用核心：
// 内置实现覆盖"本地文件 / 控制台 / 任何字节流"场景，
// 远程存储类 Sink 若按字节流方式接入（内部自行批量化）也可直接复用本核心。
type ioCore struct {
	level   LevelEnabler // 级别开关（通常是共享的 AtomicLevel）
	encoder Encoder      // 编码器（无状态可共享）
	sink    Sink         // 字节流目的地
	fields  []Field      // With 注入的上下文字段（zap 模型：固定字段由核心持有）
}

// NewCore 构造一个通用核心。
func NewCore(level LevelEnabler, enc Encoder, sink Sink) Core {
	return &ioCore{level: level, encoder: enc, sink: sink}
}

// Enabled 实现 Core。
func (c *ioCore) Enabled(lv Level) bool { return c.level.Enabled(lv) }

// Write 实现 Core：池化缓冲编码 → 写 sink。
func (c *ioCore) Write(entry *Entry, fields []Field) error {
	if len(c.fields) > 0 {
		// 三段切片强制复制，避免 append 写入 c.fields 底层数组污染兄弟克隆
		fields = append(c.fields[:len(c.fields):len(c.fields)], fields...)
	}
	buf := getBuffer()
	c.encoder.Encode(buf, entry, fields)
	var err error
	if buf.Len() > 0 {
		_, err = c.sink.Write(buf.Bytes())
	}
	putBuffer(buf)
	return err
}

// Sync 实现 Core。
func (c *ioCore) Sync() error { return c.sink.Sync() }

// Close 实现 Core。
func (c *ioCore) Close() error { return c.sink.Close() }

// With 实现 Core：返回携带固定字段的克隆（共享底层 sink/encoder/level）。
func (c *ioCore) With(fields []Field) Core {
	clone := *c
	clone.fields = append(clone.fields[:len(clone.fields):len(clone.fields)], fields...)
	return &clone
}

// teeCore 多路核心：一条日志事件同时交给所有子核心（各自再做级别过滤）。
// 对应 zapcore.NewTee。
type teeCore struct {
	cores []Core
}

// NewTee 把多个核心合并为一个；只传一个时直接返回该核心避免多余包装。
func NewTee(cores ...Core) Core {
	switch len(cores) {
	case 0:
		return nopCore{}
	case 1:
		return cores[0]
	default:
		return teeCore{cores: append([]Core(nil), cores...)}
	}
}

// Enabled 实现 Core：任一子核心启用即启用（否则日志事件根本不会到达这里）。
func (t teeCore) Enabled(lv Level) bool {
	for _, c := range t.cores {
		if c.Enabled(lv) {
			return true
		}
	}
	return false
}

// Write 实现 Core：逐子核心判断各自级别后写出，
// 复刻 zap CheckedEntry 语义——每路输出独立过滤自己的级别。
func (t teeCore) Write(entry *Entry, fields []Field) error {
	for _, c := range t.cores {
		if !c.Enabled(entry.Level) {
			continue
		}
		if err := c.Write(entry, fields); err != nil {
			return err
		}
	}
	return nil
}

// Sync 实现 Core。
func (t teeCore) Sync() error {
	var firstErr error
	for _, c := range t.cores {
		if err := c.Sync(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close 实现 Core。
func (t teeCore) Close() error {
	var firstErr error
	for _, c := range t.cores {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// With 实现 Core：向所有子核心注入固定字段。
func (t teeCore) With(fields []Field) Core {
	cores := make([]Core, len(t.cores))
	for i, c := range t.cores {
		cores[i] = c.With(fields)
	}
	return teeCore{cores: cores}
}

// nopCore 空核心：全部输出关闭时使用，语义清晰且每条调用零成本。
type nopCore struct{}

// Enabled 实现 Core。
func (nopCore) Enabled(Level) bool { return false }

// Write 实现 Core。
func (nopCore) Write(*Entry, []Field) error { return nil }

// Sync 实现 Core。
func (nopCore) Sync() error { return nil }

// Close 实现 Core。
func (nopCore) Close() error { return nil }

// With 实现 Core。
func (nopCore) With([]Field) Core { return nopCore{} }

// Hook 日志事件回调：每条被某核心处理的日志触发一次，err 非 nil 时
// 由 Logger 记录到 errorOutput（控制台 stderr）。常用于埋点上报、
// 联动告警（如 Error 级别推送到 IM）。对应 zap 的 zapcore.Hook。
type Hook func(entry *Entry) error

// hooksCore 给任意核心附加 Hook 的装饰器。
type hooksCore struct {
	Core
	hooks []Hook
}

// AddHook 给 core 追加回调并返回装饰后的核心。
func AddHook(core Core, hooks ...Hook) Core {
	if hc, ok := core.(*hooksCore); ok {
		hc.hooks = append(hc.hooks, hooks...)
		return hc
	}
	return &hooksCore{Core: core, hooks: append([]Hook(nil), hooks...)}
}

// Write 实现 Core。
func (c *hooksCore) Write(entry *Entry, fields []Field) error {
	if err := c.Core.Write(entry, fields); err != nil {
		return err
	}
	var firstErr error
	for _, h := range c.hooks {
		if err := h(entry); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// With 实现 Core。
func (c *hooksCore) With(fields []Field) Core {
	return &hooksCore{Core: c.Core.With(fields), hooks: c.hooks}
}

// SamplingConfig 采样配置：对同一消息的重复日志做限流，防止
// 高频重复日志（如每请求打印同一句）刷爆磁盘。语义与 zap sampler 相同：
// 同一分钟内，同一消息前 Initial 条全量记录，之后每 Thereafter 条记录 1 条。
type SamplingConfig struct {
	Initial    int // 每分钟每种消息的初始全量记录条数，<=0 视为 1
	Thereafter int // 之后每 N 条记录 1 条，<=0 视为 100（即基本只留 Initial）
}

// samplerCore 采样核心：锁-free 计数表（级别 × fnv32a 哈希槽），
// 计数与窗口重置全走原子操作（zap sampler.go 同款算法，避免互斥锁竞争）。
// 窗口固定 1 分钟：对同一消息，前 Initial 条全量记录，之后每 Thereafter 条记录 1 条。
type samplerCore struct {
	Core
	clock      func() time.Time
	initial    uint32
	thereafter uint32
	counters   sampleCounters // 固定数组内嵌，无锁竞争重建问题
}

// _numLevels 计数表按级别分桶；_countersPerLevel 每级别的哈希槽数
// （power of two，fnv32a 哈希足够均匀时冲突概率低；冲突只是多记几条，无正确性问题）。
const (
	_numLevels        = int(maxLevel - minLevel + 1)
	_countersPerLevel = 64
)

// sampleCounters 计数表：每个槽自带过期时间戳，过期即原子重置。
type sampleCounters struct {
	arr [_numLevels][_countersPerLevel]sampleCounter
}

type sampleCounter struct {
	resetAt atomic.Int64
	counter atomic.Uint64
}

// newSamplerCore 包装 core 增加采样。
func newSamplerCore(core Core, cfg SamplingConfig, clock func() time.Time) Core {
	initial := uint32(cfg.Initial)
	if cfg.Initial <= 0 {
		initial = 1
	}
	thereafter := uint32(cfg.Thereafter)
	if cfg.Thereafter <= 0 {
		thereafter = 100
	}
	if clock == nil {
		clock = time.Now
	}
	return &samplerCore{
		Core:       core,
		clock:      clock,
		initial:    initial,
		thereafter: thereafter,
	}
}

// Enabled 实现 Core。
func (s *samplerCore) Enabled(lv Level) bool { return s.Core.Enabled(lv) }

// Write 实现 Core：先采样决策，命中才交给子核心。
func (s *samplerCore) Write(entry *Entry, fields []Field) error {
	idx := int(entry.Level - minLevel)
	// 级别越界（如自定义扩展级别）不采样，直接放行
	if idx < 0 || idx >= _numLevels {
		return s.Core.Write(entry, fields)
	}
	c := &s.counters.arr[idx][fnv32a(entry.Message)%_countersPerLevel]
	if c.incCheckReset(s.clock(), s.initial, s.thereafter) {
		return s.Core.Write(entry, fields)
	}
	return nil
}

// incCheckReset 计数并判定是否记录：1 分钟窗口内前 initial 条返回 true，
// 之后每 thereafter 条返回 1 条 true。zap counter.IncCheckReset 的移植。
func (c *sampleCounter) incCheckReset(now time.Time, initial, thereafter uint32) bool {
	tn := now.UnixNano()
	resetAfter := c.resetAt.Load()
	if resetAfter > tn {
		n := c.counter.Add(1)
		if n <= uint64(initial) {
			return true
		}
		return (n-uint64(initial))%uint64(thereafter) == 0
	}
	// 窗口过期：重置计数，本条记为窗口第 1 条
	c.counter.Store(1)
	newResetAfter := tn + int64(time.Minute)
	if !c.resetAt.CompareAndSwap(resetAfter, newResetAfter) {
		// 与其他 goroutine 的窗口重置竞争：对方已重置成功，本条补计一次
		n := c.counter.Add(1)
		return n <= uint64(initial)
	}
	return true
}

// fnv32a FNV-1a 哈希，去掉 []byte 转换的分配（zap 同款实现）。
func fnv32a(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	hash := uint32(offset32)
	for i := 0; i < len(s); i++ {
		hash ^= uint32(s[i])
		hash *= prime32
	}
	return hash
}

// Sync 实现 Core。
func (s *samplerCore) Sync() error { return s.Core.Sync() }

// Close 实现 Core。
func (s *samplerCore) Close() error { return s.Core.Close() }

// With 实现 Core：派生克隆携带新上下文，计数表从零开始
// （zap sampler 同款做法：不复制内嵌原子计数器，避免锁拷贝）。
func (s *samplerCore) With(fields []Field) Core {
	return &samplerCore{
		Core:       s.Core.With(fields),
		clock:      s.clock,
		initial:    s.initial,
		thereafter: s.thereafter,
	}
}
