// 示例三（base3）：base2 的 JSON 形态 —— 同样的测试代码，打印日志内容改为 JSON 格式。
//
// 与 base2 的唯一差别是输出格式：控制台与 .log 文件均为"一行一个 JSON 对象"。
// 注意两点：
//   - 旧版 Config 字段下控制台 sink 固定为 console 编码，Encoding 只作用于文件；
//     要让控制台也输出 JSON，需用 Sinks 列表显式声明两路输出；
//   - JSON 编码器不着色（ANSI 颜色只属于 console 编码器），这是管道场景的
//     预期取舍；滚动切割参数与 base2 完全一致（lumberjack 等价）。
//
// 运行方式（在 example 目录下）：
//
//	go run ./base3
//
// 说明：日志文件生成在本目录 logs/ 下（已被主仓库 .gitignore 忽略）。
package main

import (
	"errors"
	"fmt"

	"github.com/hicode0101/go-logger"
)

const demoFile = "logs/base3-demo.log"

func main() {

	// ---------------------------------------------------------------------------
	// 正式演示配置：控制台 + 文件双路 JSON——终端与文件里都是一行一个 JSON 对象
	// level/time/logger/caller/msg 及 Error 自动附带的 stacktrace 全部为字段
	// ---------------------------------------------------------------------------
	fmt.Println("========== 双路 JSON 配置：控制台 + 文件 ==========")
	_ = logger.Init(logger.Config{
		Level: "debug",
		Sinks: []logger.SinkConfig{
			{Type: "console", Format: "json"},
			{Type: "file", Format: "json", Path: demoFile, // file 专用滚动参数（lumberjack 等价）
				MaxSizeMB: 1, MaxBackups: 3, MaxAgeDays: 7, Compress: false, LocalTime: true},
		},
	})

	logger.Debug("Debug：开发期细节，生产环境会被级别过滤")
	logger.Info("Info：业务关键节点打点 user=%s", "Tom")
	logger.Warn("Warn：可自愈的异常")
	logger.Error("Error：error 及以上级别自动附带调用堆栈")
	logger.ErrorWithErr("读取配置失败", errors.New("file not found"))

	httpLog := logger.Named("http")
	httpLog.Info("listening on :8080")
	_ = logger.Sync()

	_ = logger.Close()
}
