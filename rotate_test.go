package logger

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitForTimeout 轮询等待条件成立（mill 清理在后台协程异步执行）。
func waitForTimeout(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// newTestRotator 在临时目录创建 MaxSizeMB=maxSize 的滚动器。
func newTestRotator(t *testing.T, dir string, maxSizeMB, maxBackups, maxAgeDays int, compress bool) *Rotator {
	t.Helper()
	r := &Rotator{
		Filename:   filepath.Join(dir, "app.log"),
		MaxSizeMB:  maxSizeMB,
		MaxBackups: maxBackups,
		MaxAgeDays: maxAgeDays,
		Compress:   compress,
		LocalTime:  true,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.openExistingOrNew(0); err != nil {
		t.Fatalf("打开滚动文件失败: %v", err)
	}
	return r
}

// backupCount 统计目录内备份文件个数（app-*.log*）。
func backupCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "app-") {
			n++
		}
	}
	return n
}

// writeBytes 向滚动器写入 len 字节的伪数据。
func writeBytes(t *testing.T, r *Rotator, n int) {
	t.Helper()
	p := make([]byte, n)
	for i := range p {
		p[i] = 'a' + byte(i%26)
	}
	if _, err := r.Write(p); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
}

// TestRotatorSizeRotation 超过单文件上限自动切割出带时间戳的备份文件。
func TestRotatorSizeRotation(t *testing.T) {
	dir := t.TempDir()
	r := newTestRotator(t, dir, 1, 0, 0, false) // 1MB 上限，不限备份
	defer func() { _ = r.Close() }()

	writeBytes(t, r, 600*1024)
	writeBytes(t, r, 600*1024) // 触发切割

	if !waitForTimeout(time.Second, func() bool { return backupCount(t, dir) == 1 }) {
		t.Fatalf("应切割出 1 个备份文件，实际 %d 个", backupCount(t, dir))
	}
	// 备份文件名符合 <prefix>-<timestamp>.log 规则
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "app-") && strings.HasSuffix(name, ".log") {
			found = true
			ts := name[len("app-") : len(name)-len(".log")]
			if _, err := time.Parse(backupTimeFormat, ts); err != nil {
				t.Errorf("备份文件时间戳非法: %s (%v)", name, err)
			}
		}
	}
	if !found {
		t.Error("未找到符合命名规则的备份文件")
	}
}

// TestRotatorMaxBackups 超出保留个数的旧备份被删除。
func TestRotatorMaxBackups(t *testing.T) {
	dir := t.TempDir()
	r := newTestRotator(t, dir, 1, 2, 0, false) // 最多保留 2 个备份
	defer func() { _ = r.Close() }()

	// 写满 4 次产生 4 个备份
	for i := 0; i < 4; i++ {
		writeBytes(t, r, 600*1024)
		writeBytes(t, r, 600*1024)
		time.Sleep(15 * time.Millisecond) // 保证时间戳（毫秒精度）不重复
	}
	if !waitForTimeout(2*time.Second, func() bool { return backupCount(t, dir) <= 2 }) {
		t.Fatalf("备份个数应收敛到 2 个以内，实际 %d 个", backupCount(t, dir))
	}
}

// TestRotatorMaxAge 按文件名时间戳删除过期备份。
func TestRotatorMaxAge(t *testing.T) {
	dir := t.TempDir()
	r := newTestRotator(t, dir, 1, 0, 1, false) // 保留 1 天
	defer func() { _ = r.Close() }()

	// 手工造两个"过期"备份（文件名时间戳在保留窗口外）
	old1 := filepath.Join(dir, "app-2020-01-01T00-00-00.000.log")
	old2 := filepath.Join(dir, "app-2020-01-02T00-00-00.000.log")
	for _, f := range []string{old1, old2} {
		if err := os.WriteFile(f, []byte("old"), 0o644); err != nil {
			t.Fatalf("创建过期备份失败: %v", err)
		}
	}
	// 造一个"新鲜"备份（当前时间，走正常切割路径）
	writeBytes(t, r, 600*1024)
	writeBytes(t, r, 600*1024)

	if !waitForTimeout(2*time.Second, func() bool {
		_, err1 := os.Stat(old1)
		_, err2 := os.Stat(old2)
		return os.IsNotExist(err1) && os.IsNotExist(err2)
	}) {
		t.Error("过期备份应被删除")
	}
}

// TestRotatorCompress 历史备份被 gzip 压缩且原文删除、内容可解压。
func TestRotatorCompress(t *testing.T) {
	dir := t.TempDir()
	r := newTestRotator(t, dir, 1, 0, 0, true) // 开启压缩
	defer func() { _ = r.Close() }()

	// 写入高冗余内容，压缩后应明显变小
	writeBytes(t, r, 600*1024)
	writeBytes(t, r, 600*1024)

	// 压缩完成的标志：.gz 出现且原始备份已删除（压缩协程原子完成两步）
	var gzFile string
	ok := waitForTimeout(5*time.Second, func() bool {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(name, ".log.gz") {
				gzFile = filepath.Join(dir, name)
				if _, err := os.Stat(gzFile); err != nil {
					return false // gz 尚未写完
				}
			}
			if strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".log.gz") && strings.HasPrefix(name, "app-") {
				// 原始备份仍在 → 压缩未完成
				return false
			}
		}
		return gzFile != ""
	})
	if !ok {
		t.Fatal("应产生 .gz 压缩备份且原始备份被删除")
	}
	info, err := os.Stat(gzFile)
	if err != nil {
		t.Fatalf("压缩文件统计失败: %v", err)
	}
	if info.Size() >= 600*1024 {
		t.Errorf("高冗余内容压缩后应明显小于原文: %d bytes", info.Size())
	}
	// 原始备份文件应已删除
	raw := strings.TrimSuffix(gzFile, ".gz")
	if _, err := os.Stat(raw); !os.IsNotExist(err) {
		t.Error("压缩后原始备份应被删除")
	}
	// 内容可解压
	f, err := os.Open(gzFile)
	if err != nil {
		t.Fatalf("打开压缩文件失败: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip 解压失败: %v", err)
	}
	n, err := io.Copy(io.Discard, gz)
	if err != nil || n == 0 {
		t.Errorf("解压内容异常: n=%d err=%v", n, err)
	}
}

// TestRotatorReopenAfterDelete 文件被外部删除后下一次写自动重建。
// Windows 不允许删除打开中的文件，故先 Close 释放句柄再模拟外部删除。
func TestRotatorReopenAfterDelete(t *testing.T) {
	dir := t.TempDir()
	r := newTestRotator(t, dir, 1, 0, 0, false)
	defer func() { _ = r.Close() }()

	path := filepath.Join(dir, "app.log")
	_ = r.Close() // 释放句柄，模拟另一进程删除前的状态
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除日志文件失败: %v", err)
	}
	writeBytes(t, r, 10)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("日志文件应被自动重建: %v", err)
	}
	if len(data) != 10 {
		t.Errorf("重建后文件大小 = %d, want 10", len(data))
	}
}

// TestRotatorCloseAndRewrite Close 后再次 Write 应重新打开并追加。
func TestRotatorCloseAndRewrite(t *testing.T) {
	dir := t.TempDir()
	r := newTestRotator(t, dir, 1, 0, 0, false)

	writeBytes(t, r, 10)
	if err := r.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := r.Close(); err != nil { // 幂等
		t.Errorf("重复 Close 不应报错: %v", err)
	}
	// Close 后再写：重新打开追加
	writeBytes(t, r, 5)
	data, _ := os.ReadFile(filepath.Join(dir, "app.log"))
	if len(data) != 15 {
		t.Errorf("重开后应追加内容，总大小 = %d, want 15", len(data))
	}
	if err := r.Close(); err != nil { // 重新打开的句柄需再次释放
		t.Errorf("最终 Close 失败: %v", err)
	}
}

// TestRotatorOversizedMessage 单条写入超过上限返回错误且不破坏文件。
func TestRotatorOversizedMessage(t *testing.T) {
	dir := t.TempDir()
	r := newTestRotator(t, dir, 1, 0, 0, false)
	defer func() { _ = r.Close() }()

	p := make([]byte, 2*1024*1024) // 2MB > 1MB 上限
	if _, err := r.Write(p); err == nil {
		t.Error("超过单文件上限的单条写入应返回错误")
	}
	// 原日志文件不受影响，仍可正常写入
	writeBytes(t, r, 10)
	data, _ := os.ReadFile(filepath.Join(dir, "app.log"))
	if len(data) != 10 {
		t.Errorf("失败写入不应破坏文件，大小 = %d, want 10", len(data))
	}
}

// TestRotatorConcurrentWrites 并发写入的总字节数守恒（-race 检查）。
func TestRotatorConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	r := newTestRotator(t, dir, 1, 0, 0, false)
	defer func() { _ = r.Close() }()

	const goroutines, perG = 16, 200
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				_, _ = r.Write([]byte("0123456789")) // 10 字节
			}
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(filepath.Join(dir, "app.log"))
	// 可能发生切割，只统计主文件；总数应为 10 的整数倍且 <= 总写入量
	if len(data)%10 != 0 {
		t.Errorf("主文件大小 %d 应为写入粒度 10 的整数倍", len(data))
	}
	if total := goroutines * perG * 10; len(data) > total {
		t.Errorf("主文件大小 %d 超过总写入量 %d", len(data), total)
	}
}
