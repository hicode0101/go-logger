package logger

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// nlSink 收集写入内容的内存 sink（NewLogger 测试专用）。
type nlSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *nlSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *nlSink) Sync() error  { return nil }
func (s *nlSink) Close() error { return nil }

func (s *nlSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestNewLoggerCustomCore 验证自定义 Core 经 NewLogger 接入后，
// 实例方法与全局 logger 行为一致：默认记录 caller、级别过滤、With 上下文。
func TestNewLoggerCustomCore(t *testing.T) {
	sink := &nlSink{}
	log := NewLogger(NewCore(NewAtomicLevel(InfoLevel), NewJSONEncoder(EncoderConfig{}), sink))

	log.Info("custom-core-hello", String("k", "v"))
	out := sink.String()
	if !strings.Contains(out, `"msg":"custom-core-hello"`) ||
		!strings.Contains(out, `"k":"v"`) {
		t.Fatalf("实例 Info 输出不符合 JSON 行预期：%s", out)
	}
	if !strings.Contains(out, `"caller"`) {
		t.Fatalf("NewLogger 默认应记录 caller：%s", out)
	}

	// 低于实例级别（info）的 Debug 被过滤，不产生输出
	before := sink.String()
	log.Debug("should be filtered")
	if sink.String() != before {
		t.Fatalf("Debug 不应被输出：%s", sink.String())
	}

	// With 派生携带固定字段
	log.With(String("module", "pay")).Warn("with-context")
	out = sink.String()
	if !strings.Contains(out, `"module":"pay"`) || !strings.Contains(out, `"level":"warn"`) {
		t.Fatalf("With 派生实例的上下文字段缺失：%s", out)
	}

	// 释放资源
	if err := log.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}
}
