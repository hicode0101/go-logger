package logger_test

import (
	"errors"
	"fmt"

	gologger "github.com/hicode0101/go-logger"
)

// Example 包级 API 基本用法：import 即用，无需初始化。
func Example() {
	gologger.Info("用户 %s 登录成功", "Tom")
	gologger.Warn("磁盘使用率 %v%%", 85.5)
	gologger.ErrorWithErr("读取配置失败", errors.New("file not found"))
	gologger.Infow("下单成功", "orderNo", "NO20240101", "amount", 99.5)
	// Output:
}

// ExampleLogger_named 子日志器按模块区分。
func ExampleLogger_named() {
	httpLog := gologger.Named("http")
	dbLog := httpLog.Named("db") // 二级名称 http.db
	_ = dbLog
	httpLog.Info("listening on :8080")
	// Output:
}

// ExampleLogger_withFields 请求上下文注入。
func ExampleLogger_withFields() {
	reqLog := gologger.WithFields(map[string]interface{}{
		"requestId": "req-9f8e7d",
	})
	reqLog.Info("开始处理下单")
	// Output:
}

// ExampleInit 自定义配置：JSON 文件 + 多路输出。
func ExampleInit() {
	cfg := gologger.DefaultConfig()
	cfg.Level = "info"
	cfg.Console = false
	cfg.Encoding = "json"
	cfg.FilePath = "logs/app.json.log"
	cfg.MaxSizeMB = 100
	cfg.Compress = true
	if err := gologger.Init(cfg); err != nil {
		fmt.Println("init:", err)
		return
	}
	gologger.Info("json 输出")
	_ = gologger.Close()
	// Output:
}
