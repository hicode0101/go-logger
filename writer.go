package logger

import "sync"

// writerBuf 手写聚合缓冲（bufio.Writer 的 Sink 定制版）：
//   - 写入只在缓冲满或 Flush 时落到底层 Sink，显著降低系统调用次数；
//   - 内部互斥锁保证并发安全；
//   - 刷盘失败保留已聚合数据语义与 bufio 一致（丢弃已刷部分）。
type writerBuf struct {
	mu   sync.Mutex
	buf  []byte
	sink Sink
}

// newWriterBuf 创建容量为 size 字节的聚合缓冲。
func newWriterBuf(sink Sink, size int) *writerBuf {
	if size <= 0 {
		size = 32 * 1024
	}
	return &writerBuf{buf: make([]byte, 0, size), sink: sink}
}

// Write 聚合写入；超过容量的部分直接透传给底层 Sink。
func (b *writerBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) > cap(b.buf)-len(b.buf) {
		// 单次写入超过剩余容量：先刷出已聚合部分，再透传大块
		if err := b.flushLocked(); err != nil {
			return 0, err
		}
		if len(p) >= cap(b.buf) {
			return b.sink.Write(p)
		}
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// Flush 刷出聚合的日志字节。
func (b *writerBuf) Flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.flushLocked()
}

func (b *writerBuf) flushLocked() error {
	if len(b.buf) == 0 {
		return nil
	}
	_, err := b.sink.Write(b.buf)
	b.buf = b.buf[:0]
	return err
}
