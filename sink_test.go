package logger

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRegisterSinkAndUnknownType 注册/覆盖 sink 工厂；未知类型返回错误。
func TestRegisterSinkAndUnknownType(t *testing.T) {
	// 未知类型
	_, err := newSink(SinkConfig{Type: "no-such-sink"})
	if err == nil || !strings.Contains(err.Error(), "no-such-sink") {
		t.Errorf("未知 sink 类型应报错并包含类型名: %v", err)
	}

	// 注册测试类型
	RegisterSink("testfake", func(cfg SinkConfig) (Sink, error) {
		return &memSink{}, nil
	})
	s, err := newSink(SinkConfig{Type: "testfake"})
	if err != nil {
		t.Fatalf("注册类型应可构造: %v", err)
	}
	if _, err := s.Write([]byte("x")); err != nil {
		t.Errorf("写入失败: %v", err)
	}
}

// TestBufferedSink 覆盖缓冲写的聚合/自动刷盘/手动刷盘/Close 刷盘。
func TestBufferedSink(t *testing.T) {
	under := &memSink{}
	bs := newBufferedSink(under, 64, 0).(*bufferedSink) // 64B 缓冲，无后台刷盘

	// 小块写入留在缓冲内
	if _, err := bs.Write([]byte("short")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if under.String() != "" {
		t.Errorf("缓冲内数据不应提前落盘: %q", under.String())
	}
	// Sync 强制刷出
	if err := bs.Sync(); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if under.String() != "short" {
		t.Errorf("Sync 应刷出缓冲: %q", under.String())
	}
	// 超过容量自动刷盘（64B 缓冲写 100B）
	big := make([]byte, 100)
	if _, err := bs.Write(big); err != nil {
		t.Fatalf("大块写入失败: %v", err)
	}
	if under.String() == "" {
		t.Error("超过容量的大块应直接透传")
	}
	// Close 刷出残留
	if _, err := bs.Write([]byte("tail")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := bs.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if !strings.HasSuffix(under.String(), "tail") {
		t.Errorf("Close 应刷出残留缓冲: %q", under.String())
	}
}

// TestBufferedSinkBackgroundFlush 后台定时刷盘周期性落盘。
func TestBufferedSinkBackgroundFlush(t *testing.T) {
	under := &memSink{}
	bs := newBufferedSink(under, 1024, 1).(*bufferedSink) // 1s 后台刷盘

	if _, err := bs.Write([]byte("background")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	ok := waitForTimeout(2*time.Second, func() bool { return under.String() != "" })
	if !ok {
		t.Error("后台刷盘应在周期内落盘")
	}
	if err := bs.Close(); err != nil {
		t.Errorf("Close 失败: %v", err)
	}
}

// TestLockedSink 并发互斥包装的正确性。
func TestLockedSink(t *testing.T) {
	under := &memSink{}
	ls := NewLockedSink(under)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = ls.Write([]byte("ab"))
			}
		}()
	}
	wg.Wait()
	if got := len(under.String()); got != 8*100*2 {
		t.Errorf("并发写入总字节数 = %d, want %d", got, 8*100*2)
	}
}

// TestFileSinkViaFactory file 类型工厂创建的滚动器可写。
func TestFileSinkViaFactory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	s, err := newSink(SinkConfig{Type: "file", Path: path, MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("file sink 构建失败: %v", err)
	}
	if _, err := s.Write([]byte("file-sink-test\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !strings.Contains(string(data), "file-sink-test") {
		t.Errorf("文件内容缺失: %q", data)
	}
}

// TestParseSinkURL URL 配置解析辅助函数。
func TestParseSinkURL(t *testing.T) {
	u, q, err := parseSinkURL("file:///var/log/app.log?maxsize=100")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if u.Scheme != "file" || q.Get("maxsize") != "100" {
		t.Errorf("解析结果异常: %v %v", u.Scheme, q)
	}
}

// TestUnknownSinkTypeErrorText 错误信息可读性。
func TestUnknownSinkTypeErrorText(t *testing.T) {
	err := errUnknownSinkType("kafka")
	if !strings.Contains(err.Error(), "RegisterSink") {
		t.Errorf("错误信息应提示注册方式: %v", err)
	}
}
