package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"
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
// 动态调级别 -> Close 落盘 -> 校验文件内容。
func TestInitAndLog(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.FilePath = filepath.Join(dir, "app.log")
	if err := Init(cfg); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}

	Info("服务启动，端口=%d", 8080)
	Info("用户 %s 下单成功，订单号 %s", "Tom", "NO20240101")
	Warn("磁盘使用率 %v%%", 85.5)
	Error("数据库连接超时")
	ErrorWithErr("读取配置失败", os.ErrNotExist)
	Debug(12345) // 非字符串消息

	if GetLevel() != zapcore.DebugLevel {
		t.Errorf("默认级别应为 debug，实际 %v", GetLevel())
	}

	// 运行期调级别：debug 日志应被过滤
	if err := SetLevel("warn"); err != nil {
		t.Fatalf("SetLevel 失败: %v", err)
	}
	if GetLevel() != zapcore.WarnLevel {
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
}
