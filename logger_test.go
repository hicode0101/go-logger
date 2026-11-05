package logger

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestFormatMessage 覆盖 formatMessage 的全部分支：
// 纯文本、fmt 格式串、无占位符带参数、非字符串消息。
func TestFormatMessage(t *testing.T) {
	cases := []struct {
		name string
		f    interface{}
		v    []interface{}
		want string
	}{
		{"纯文本无参数", "hello", nil, "hello"},
		{"格式串", "user %s id=%d", []interface{}{"tom", 1}, "user tom id=1"},
		{"无占位符带参数", "count:", []interface{}{42}, "count: 42"},
		{"多个无占位符参数", "sum:", []interface{}{1, 2}, "sum: 1 2"},
		{"非字符串", 100, nil, "100"},
		{"error 对象", os.ErrNotExist, nil, "file does not exist"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatMessage(c.f, c.v...); got != c.want {
				t.Errorf("formatMessage(%v, %v) = %q, want %q", c.f, c.v, got, c.want)
			}
		})
	}
}

// TestInitAndLog 演示完整初始化流程：自定义文件路径 -> 写各级别日志 ->
// 动态调级别 -> Close 落盘 -> 校验文件内容（旧版语义回归）。
func TestInitAndLog(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false // 测试环境不刷屏
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}

	Info("服务启动，端口=%d", 8080)
	Info("用户 %s 下单成功，订单号 %s", "Tom", "NO20240101")
	Warn("磁盘使用率 %v%%", 85.5)
	Error("数据库连接超时")
	ErrorWithErr("读取配置失败", os.ErrNotExist)
	Debug(12345) // 非字符串消息

	if GetLevel() != DebugLevel {
		t.Errorf("默认级别应为 debug，实际 %v", GetLevel())
	}

	// 运行期调级别：debug 日志应被过滤
	if err := SetLevel("warn"); err != nil {
		t.Fatalf("SetLevel 失败: %v", err)
	}
	if GetLevel() != WarnLevel {
		t.Errorf("级别应为 warn，实际 %v", GetLevel())
	}
	Debug("这条日志不应输出")

	// 刷写并释放文件句柄后校验文件内容
	if err := Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	data, err := os.ReadFile(cfg.FilePath)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	content := string(data)
	for _, want := range []string{"服务启动", "下单成功", "数据库连接超时", "读取配置失败", "12345"} {
		if !strings.Contains(content, want) {
			t.Errorf("日志文件缺少关键字 %q，实际内容：\n%s", want, content)
		}
	}
	if strings.Contains(content, "这条日志不应输出") {
		t.Error("warn 级别下 Debug 日志不应落盘")
	}
}

// TestInitInvalidLevel 校验非法级别返回错误且不影响原 logger。
func TestInitInvalidLevel(t *testing.T) {
	if err := SetLevel("not-a-level"); err == nil {
		t.Error("非法级别应返回错误")
	}
	if err := Init(Config{Level: "bogus"}); err == nil {
		t.Error("Init 非法级别应返回错误")
	}
}

// TestCallerLineAccuracy 校准 caller 帧偏移：包级 API 与 Logger 方法
// 报告的行号必须精确指向业务代码调用行。
func TestCallerLineAccuracy(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	log := GetLogger()

	// 包级 API：下一行的行号必须出现在日志里
	_, thisFile, thisLine, _ := runtime.Caller(0)
	Info("包级调用定位") // 该行行号 = thisLine + 1

	// Logger 实例方法：同样紧贴下一行
	_, _, methodLine, _ := runtime.Caller(0)
	log.Info("实例方法定位") // 该行行号 = methodLine + 1

	wantPkg := baseName(thisFile) + ":" + itoa(thisLine+1)
	wantMethod := baseName(thisFile) + ":" + itoa(methodLine+1)

	if err := Sync(); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	data, _ := os.ReadFile(cfg.FilePath)
	content := string(data)
	if !strings.Contains(content, wantPkg) {
		t.Errorf("包级 API caller 应为 %q，实际内容：\n%s", wantPkg, content)
	}
	if !strings.Contains(content, wantMethod) {
		t.Errorf("Logger 方法 caller 应为 %q，实际内容：\n%s", wantMethod, content)
	}
}

// TestStacktraceOnError error 级别自动携带堆栈且包含调用函数名。
func TestStacktraceOnError(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	Error("带堆栈的错误")

	data, _ := os.ReadFile(cfg.FilePath)
	content := string(data)
	if !strings.Contains(content, "TestStacktraceOnError") {
		t.Errorf("堆栈应包含测试函数名：\n%s", content)
	}
}

// TestDisableStacktraceOption 关闭堆栈后 error 不再携带堆栈。
func TestDisableStacktraceOption(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	cfg.DisableStacktrace = true
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	Error("无堆栈错误")
	data, _ := os.ReadFile(cfg.FilePath)
	if strings.Contains(string(data), "TestDisableStacktraceOption\n") || strings.Count(string(data), "\n\t") > 0 {
		t.Errorf("关闭堆栈后不应有堆栈内容：\n%s", data)
	}
}

// TestNamedAndWithFields 验证子日志器名与固定字段贯穿输出。
func TestNamedAndWithFields(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	cfg.Encoding = "json"
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	httpLog := Named("http")
	dbLog := httpLog.Named("db") // 二级名称 http.db
	reqLog := WithFields(map[string]interface{}{"requestId": "req-9f8e7d", "uid": 10001})

	httpLog.Info("listening")
	dbLog.Info("connected")
	reqLog.Info("处理下单")

	data, _ := os.ReadFile(cfg.FilePath)
	content := string(data)
	for _, want := range []string{`"logger":"http"`, `"logger":"http.db"`, `"requestId":"req-9f8e7d"`, `"uid":10001`} {
		if !strings.Contains(content, want) {
			t.Errorf("输出缺少 %q：\n%s", want, content)
		}
	}
}

// TestStructuredFieldsJson 强类型字段在 JSON 输出中的编码。
func TestStructuredFieldsJson(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	cfg.Encoding = "json"
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	Infow("下单成功",
		"orderNo", "NO20240101",
		"amount", 99.5,
		"count", 3,
		"paid", true,
	)
	_ = Sync()
	data, _ := os.ReadFile(cfg.FilePath)
	content := string(data)
	for _, want := range []string{`"orderNo":"NO20240101"`, `"amount":99.5`, `"count":3`, `"paid":true`} {
		if !strings.Contains(content, want) {
			t.Errorf("输出缺少 %q：\n%s", want, content)
		}
	}
}

// TestInstanceLevelFiltering 实例方法级别过滤（通过 Init 全局级别）。
func TestInstanceLevelFiltering(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	cfg.Level = "error"
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	log := GetLogger()
	log.Debug("d")
	log.Info("i")
	log.Warn("w")
	log.Error("e")

	data, _ := os.ReadFile(cfg.FilePath)
	content := string(data)
	for _, unwanted := range []string{"d", "i", "w"} {
		if strings.Contains(content, "\t"+unwanted+"\t") {
			t.Errorf("error 级别下 %s 级日志不应输出", unwanted)
		}
	}
	if !strings.Contains(content, "e") {
		t.Error("error 级别日志应输出")
	}
}

// TestSinksMultiOutput 多路输出配置：控制台 + JSON 文件同时生效。
func TestSinksMultiOutput(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "app.json.log")
	if err := Init(Config{
		Level: "debug",
		Sinks: []SinkConfig{
			{Type: "console", Format: "console", Color: true},
			{Type: "file", Format: "json", Path: filePath, MaxSizeMB: 1, MaxBackups: 3},
		},
	}); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	Info("多路输出测试")
	if err := Sync(); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("JSON 日志文件不存在: %v", err)
	}
	if !strings.Contains(string(data), `"msg":"多路输出测试"`) {
		t.Errorf("文件应为 JSON 格式：\n%s", data)
	}
}

// TestPerSinkIndependentLevel 每路输出独立级别。
func TestPerSinkIndependentLevel(t *testing.T) {
	dir := t.TempDir()
	quietPath := filepath.Join(dir, "quiet.log")
	if err := Init(Config{
		Level: "debug",
		Sinks: []SinkConfig{
			{Type: "file", Format: "console", Path: filepath.Join(dir, "main.log"), MaxSizeMB: 1},
			{Type: "file", Format: "console", Path: quietPath, MaxSizeMB: 1, Level: "error"},
		},
	}); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	Warn("warn-line")
	Error("error-line")
	if err := Sync(); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}

	quiet, _ := os.ReadFile(quietPath)
	if strings.Contains(string(quiet), "warn-line") {
		t.Error("独立级别 error 的输出不应收到 warn")
	}
	if !strings.Contains(string(quiet), "error-line") {
		t.Error("独立级别 error 的输出应收到 error")
	}
	main, _ := os.ReadFile(filepath.Join(dir, "main.log"))
	if !strings.Contains(string(main), "warn-line") {
		t.Error("默认级别输出应收到 warn")
	}
}

// TestSamplingViaConfig 配置开启采样后高频重复消息被限流。
func TestSamplingViaConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	cfg.Sampling = &SamplingConfig{Initial: 2, Thereafter: 10}
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	for i := 0; i < 20; i++ {
		Info("重复消息采样") // 同一消息
	}
	if err := Sync(); err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	data, _ := os.ReadFile(cfg.FilePath)
	n := strings.Count(string(data), "重复消息采样")
	// 前 2 条 + 之后每 10 条取 1 → 2 + 2 = 4（i=11 的 (11-2)%10==9 不中，i=12 中）
	if n >= 20 {
		t.Errorf("采样应显著减少输出条数，实际 %d 条", n)
	}
	if n < 2 {
		t.Errorf("Initial=2 至少应记录 2 条，实际 %d 条", n)
	}
}

// TestPanicBehavior Panic 级别输出日志后 panic。
func TestPanicBehavior(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	defer func() {
		if r := recover(); r != nil {
			if entry, ok := r.(Entry); ok && entry.Message != "panic-message" {
				t.Errorf("panic 携带 Entry 的 Message 不符: %v", entry.Message)
			}
			return
		}
		t.Error("Panic 级别应触发 panic")
	}()
	Panic("panic-message")
}

// TestDPanicBehavior 开发模式 DPanic panic，生产模式不 panic。
func TestDPanicBehavior(t *testing.T) {
	// 生产模式：等同 error，不 panic
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	DPanic("生产模式不 panic")
	_ = Close()

	// 开发模式：panic
	cfg.Development = true
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()
	defer func() {
		if recover() == nil {
			t.Error("开发模式下 DPanic 应 panic")
		}
	}()
	DPanic("开发模式 panic")
}

// TestFatalBehavior Fatal 输出日志后调用退出函数（测试替换为不退出）。
func TestFatalBehavior(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer func() { _ = Close() }()

	called := make(chan int, 1)
	stdLogger().exitFunc = func(code int) { called <- code }
	defer func() { stdLogger().exitFunc = os.Exit }()

	Fatal("致命错误")
	if code := <-called; code != 1 {
		t.Errorf("Fatal 退出码应为 1，实际 %d", code)
	}
}

// TestConcurrentLogging 并发写日志的总行数守恒（-race 检查）。
func TestConcurrentLogging(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}

	const goroutines, perG = 16, 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				Info("并发日志 %d-%d", i, j)
			}
		}(i)
	}
	wg.Wait()
	if err := Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	data, err := os.ReadFile(cfg.FilePath)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	lines := strings.Count(string(data), "\n")
	if lines != goroutines*perG {
		t.Errorf("日志行数 = %d, want %d（并发丢失或切割干扰）", lines, goroutines*perG)
	}
}

// TestReopenAfterFileDeleteInit 文件被删除后重建输出（经全局 API）。
func TestReopenAfterFileDeleteInit(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}

	path := cfg.FilePath
	Info("删除前")
	_ = os.Remove(path)
	Info("删除后重建")
	if err := Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("删除后应自动重建文件: %v", err)
	}
	if !strings.Contains(string(data), "删除后重建") {
		t.Errorf("重建后内容缺失：\n%s", data)
	}
}

// TestBufferedFileViaConfig 缓冲写配置经 Init 生效且 Close 时刷出。
func TestBufferedFileViaConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	cfg.Console = false
	cfg.FileBufferSizeKB = 4 // 4KB 缓冲
	cfg.FlushIntervalSec = 60
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	Info("缓冲写测试")
	// 未 Sync/Close 前内容可能仍在缓冲 —— 直接 Close 刷出
	if err := Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	data, _ := os.ReadFile(cfg.FilePath)
	if !strings.Contains(string(data), "缓冲写测试") {
		t.Errorf("Close 应刷出缓冲内容：\n%s", data)
	}
}

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

// baseName 取路径最后一段（不引入 path/filepath 到生产代码的重复依赖）。
func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	if i := strings.LastIndexByte(p, '\\'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// itoa 小整数转字符串（测试用，避免 strconv 导入噪音）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
