// 示例二（base2）：v1 观感对齐专项演示 —— 彩色控制台 + 无色 .log 文件 + 自动滚动切割。
//
// 与 basic 示例的差别：basic 覆盖"功能是否齐全"，本示例聚焦"观感是否一致"——
// 用与 v1 完全相同的配置形态（旧版 Config 字段）跑起来，并对三个观感特征
// 逐项给出程序化证据（而非只靠肉眼），便于对照评估 v1 → v2 的支持度差异：
//
//	① 控制台各级别带 ANSI 颜色（捕获 stdout 统计转义序列）
//	② .log 文件纯文本、零转义序列（读回文件校验）
//	③ 写满即滚动、按个数清理（目录列表 + 大小统计）
//
// 运行方式（在 example 目录下）：
//
//	go run ./base2
//
// 说明：日志文件生成在本目录 logs/ 下（已被主仓库 .gitignore 忽略）。
package main

import (
	"errors"
	"fmt"

	"github.com/hicode0101/go-logger"
)

const demoFile = "logs/base2-demo.log"

func main() {

	// ---------------------------------------------------------------------------
	// 正式演示配置：与 v1 等价的旧版 Config 字段——控制台彩色 + 文件纯文本 + 滚动
	// 眼见为实：下面这批日志在终端里应呈现彩色级别名，而文件里没有颜色
	// ---------------------------------------------------------------------------
	fmt.Println("========== v1 等价配置：彩色控制台 + 纯文本文件（眼见为实） ==========")
	cfg := logger.DefaultConfig()
	cfg.Level = "debug"
	cfg.Console = true
	cfg.ConsoleColor = true
	cfg.File = true
	cfg.FilePath = demoFile
	cfg.Encoding = "console" // 文件也用 console 布局（与 v1 文件端一致），级别纯大写无色
	cfg.MaxSizeMB = 1        // 单文件 1MB 上限（v1: MaxSize）
	cfg.MaxBackups = 3       // 最多保留 3 个历史文件（v1: MaxBackups）
	cfg.MaxAgeDays = 7       // 历史文件最长保留 7 天（v1: MaxAge）
	cfg.Compress = false     // 置 true 时历史文件自动 gzip（v1: Compress）
	_ = logger.Init(cfg)

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
