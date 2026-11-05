package logger

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
)

// TestDefaultLoggerLazyInit 回归测试：包级 API 在"全局日志器尚未初始化"时
// 必须自动懒初始化默认配置，而不是静默丢弃日志。
//
// 背景：旧实现把默认初始化放在包级 init() 中，但 Go 按文件名字母序执行包内
// init——logger.go 先于 sink.go 执行，Init 因 console/file Sink 工厂未注册而
// 失败且错误被吞，导致"import 即用"整体静默（既有测试全部显式 Init，未暴露）。
// 本测试复现故障现场（std 为空 + 懒初始化 Once 未消费），验证包级调用可自愈。
func TestDefaultLoggerLazyInit(t *testing.T) {
	// 1. 复现故障现场：全局日志器为空、懒初始化 Once 未消费
	std.Store(nil)
	defaultInitOnce = sync.Once{}

	// 2. 重定向 stdout（console 工厂在构造时捕获 os.Stdout，需在懒初始化前换入）
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	var (
		captured bytes.Buffer
		wg       sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&captured, r)
	}()

	// 3. 不经任何显式 Init 直接包级调用（触发懒初始化路径）
	Info("lazy-init-probe-message")

	// 4. 还原 stdout 并收集输出
	os.Stdout = oldStdout
	_ = w.Close()
	wg.Wait()
	_ = r.Close()

	if !bytes.Contains(captured.Bytes(), []byte("lazy-init-probe-message")) {
		t.Fatalf("包级 Info 未输出到控制台（懒初始化未生效），实际输出：\n%s", captured.String())
	}
	if !bytes.Contains(captured.Bytes(), []byte("INFO")) {
		t.Fatalf("输出缺少级别字段，实际输出：\n%s", captured.String())
	}

	// 5. 清理：重建默认日志器，避免把测试内 stdout 句柄留给后续测试
	_ = Init(DefaultConfig())
}
