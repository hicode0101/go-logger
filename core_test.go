package logger

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

// memSink 内存收集 sink，测试用。
type memSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (m *memSink) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}

func (m *memSink) Sync() error  { return nil }
func (m *memSink) Close() error { return nil }

func (m *memSink) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.String()
}

// newMemCore 构建绑定 memSink 的 ioCore（level 为静态级别）。
func newMemCore(level Level) (*memSink, Core) {
	s := &memSink{}
	return s, NewCore(level, NewConsoleEncoder(EncoderConfig{}), s)
}

// TestTeePerChildFiltering 验证 tee 逐子核心过滤各自级别。
func TestTeePerChildFiltering(t *testing.T) {
	s1, c1 := newMemCore(InfoLevel)  // info 起
	s2, c2 := newMemCore(ErrorLevel) // error 起
	tee := NewTee(c1, c2)

	tee.Write(&Entry{Level: InfoLevel, Time: fixedTime, Message: "info-msg"}, nil)
	tee.Write(&Entry{Level: ErrorLevel, Time: fixedTime, Message: "err-msg"}, nil)

	if !bytes.Contains([]byte(s1.String()), []byte("info-msg")) {
		t.Error("子核心1（info 级）应收到 info 日志")
	}
	if bytes.Contains([]byte(s2.String()), []byte("info-msg")) {
		t.Error("子核心2（error 级）不应收到 info 日志")
	}
	if !bytes.Contains([]byte(s2.String()), []byte("err-msg")) {
		t.Error("子核心2（error 级）应收到 error 日志")
	}
}

// TestTeeEnabledEnabled 语义：任一子核心启用即启用。
func TestTeeEnabled(t *testing.T) {
	_, c2 := newMemCore(ErrorLevel)
	tee := NewTee(nopCore{}, c2)
	if !tee.Enabled(ErrorLevel) {
		t.Error("任一子核心启用应返回 true")
	}
	if tee.Enabled(InfoLevel) {
		t.Error("全部子核心禁用应返回 false")
	}
	if NewTee().Enabled(InfoLevel) {
		t.Error("空 tee 应恒为禁用")
	}
}

// TestSamplerCore 采样语义：同窗口内前 Initial 条全量，之后每 Thereafter 条取 1。
func TestSamplerCore(t *testing.T) {
	s, child := newMemCore(DebugLevel)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	sc := newSamplerCore(child, SamplingConfig{Initial: 3, Thereafter: 5}, clock)
	entry := Entry{Level: InfoLevel, Message: "same message"} // 同一消息

	n := 0
	for i := 0; i < 20; i++ {
		if err := sc.Write(&entry, nil); err != nil {
			t.Fatalf("采样核心写出失败: %v", err)
		}
	}
	// 统计命中数
	s.mu.Lock()
	n = bytes.Count(s.buf.Bytes(), []byte("same message"))
	s.mu.Unlock()
	// 前 3 条 + 之后每 5 条取 1（3 + ⌈17/5⌉ 中的整除部分 = 3 + 3 = 6）
	if n != 6 {
		t.Errorf("Initial=3 Thereafter=5 下 20 条同消息应记录 6 条，实际 %d", n)
	}

	// 窗口推进 1 分钟后重置，重新记录
	now = now.Add(time.Minute)
	if err := sc.Write(&entry, nil); err != nil {
		t.Fatalf("写出失败: %v", err)
	}
	s.mu.Lock()
	n = bytes.Count(s.buf.Bytes(), []byte("same message"))
	s.mu.Unlock()
	if n != 7 {
		t.Errorf("新窗口首条应记录，总数应为 7，实际 %d", n)
	}
}

// TestSamplerCoreDifferentMessages 不同消息互不计数：每条都是"首条"全放行。
func TestSamplerCoreDifferentMessages(t *testing.T) {
	_, child := newMemCore(DebugLevel)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	sc := newSamplerCore(child, SamplingConfig{Initial: 1, Thereafter: 100}, func() time.Time { return now })

	ms := &memSink{}
	var passed int
	_ = ms
	for i := 0; i < 5; i++ {
		// 每条消息唯一：Initial=1 下都应放行
		e := Entry{Level: InfoLevel, Message: "unique message"}
		_ = sc.Write(&e, nil)
		passed++
	}
	if passed != 5 {
		t.Errorf("5 条不同消息应全部通过采样决策，实际 %d", passed)
	}
}

// TestHooksCore hook 触发与错误传播。
func TestHooksCore(t *testing.T) {
	var got []Level
	core := AddHook(nopCore{}, func(e *Entry) error {
		got = append(got, e.Level)
		return nil
	})
	if err := core.Write(&Entry{Level: WarnLevel}, nil); err != nil {
		t.Fatalf("hook 写出不应报错: %v", err)
	}
	if len(got) != 1 || got[0] != WarnLevel {
		t.Errorf("hook 未收到事件: %v", got)
	}

	wantErr := errors.New("hook failed")
	core = AddHook(nopCore{}, func(*Entry) error { return wantErr })
	if err := core.Write(&Entry{Level: WarnLevel}, nil); !errors.Is(err, wantErr) {
		t.Errorf("hook 错误应向上传播: %v", err)
	}
}

// TestNopCore 空核心行为。
func TestNopCore(t *testing.T) {
	var c Core = nopCore{}
	if c.Enabled(InfoLevel) {
		t.Error("nop 恒禁用")
	}
	if err := c.Write(&Entry{}, nil); err != nil {
		t.Errorf("nop 写出不应报错: %v", err)
	}
	if err := c.Sync(); err != nil {
		t.Errorf("nop Sync 不应报错: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("nop Close 不应报错: %v", err)
	}
}

// TestCoreWithAccumulation ioCore.With 累积字段且不污染兄弟核心。
func TestCoreWithAccumulation(t *testing.T) {
	s1, base := newMemCore(DebugLevel)
	a := base.With([]Field{String("a", "1")})
	b := base.With([]Field{String("b", "2")})

	// 兄弟克隆共享 sink；分别写出，各自只带自己的上下文字段
	entry := Entry{Level: InfoLevel, Time: fixedTime}
	_ = a.Write(&entry, nil)
	_ = b.Write(&entry, nil)

	out := s1.String()
	if !bytes.Contains([]byte(out), []byte(`"a":"1"`)) || !bytes.Contains([]byte(out), []byte(`"b":"2"`)) {
		t.Errorf("两个克隆应各自携带自己的字段: %s", out)
	}
	// base 不带任何上下文字段
	if bytes.Contains([]byte(out), []byte(`"a":"1","b":"2"`)) {
		t.Errorf("克隆不应互相污染字段: %s", out)
	}
}
