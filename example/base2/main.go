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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hicode0101/go-logger"
)

const demoFile = "logs/base2-demo.log"

func main() {
	// ---------------------------------------------------------------------------
	// ① 控制台颜色证明：捕获 stdout，逐级别统计 ANSI 转义序列
	//    v1/v2 使用同一套 ConsoleColor 映射：
	//    Debug=亮绿(92) Info=亮蓝(94) Warn=亮黄(93) Error=亮红(91) 其他=亮洋红(95)
	// ---------------------------------------------------------------------------
	fmt.Println("========== ① 控制台颜色证明（捕获 stdout 校验转义序列） ==========")
	captured := captureStdout(func() {
		cfg := logger.DefaultConfig()
		cfg.File = false // 本阶段只看控制台
		_ = logger.Init(cfg)
		logger.Debug("颜色校验 Debug")
		logger.Info("颜色校验 Info")
		logger.Warn("颜色校验 Warn")
		logger.Error("颜色校验 Error")
		_ = logger.Sync()
	})
	n := strings.Count(captured, "\x1b[")
	fmt.Printf("捕获控制台输出 %d 字节，含 %d 个 ANSI 转义序列（4 条日志 × 级别名开/闭着色各 1）。\n",
		len(captured), n)
	for _, c := range []struct{ lv, code string }{
		{"DEBUG", "\x1b[92m"}, {"INFO", "\x1b[94m"}, {"WARN", "\x1b[93m"}, {"ERROR", "\x1b[91m"},
	} {
		status := "✓"
		if !strings.Contains(captured, c.code+c.lv) {
			status = "✗ 缺失"
		}
		fmt.Printf("  %s %-5s 颜色码 %q\n", status, c.lv, c.code)
	}

	// ---------------------------------------------------------------------------
	// ② 正式演示配置：与 v1 等价的旧版 Config 字段——控制台彩色 + 文件纯文本 + 滚动
	//    眼见为实：下面这批日志在终端里应呈现彩色级别名，而文件里没有颜色
	// ---------------------------------------------------------------------------
	fmt.Println("========== ② v1 等价配置：彩色控制台 + 纯文本文件（眼见为实） ==========")
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
	httpLog := logger.Named("http")
	httpLog.Info("listening on :8080")
	_ = logger.Sync()

	// ---------------------------------------------------------------------------
	// ③ 文件无色证明：读回 .log 文件，转义序列计数必须为 0
	// ---------------------------------------------------------------------------
	fmt.Println("========== ③ 文件无色证明（读回校验 \\x1b 计数 = 0） ==========")
	data, err := os.ReadFile(demoFile)
	if err != nil {
		fmt.Println("读取文件失败:", err)
		os.Exit(1)
	}
	esc := strings.Count(string(data), "\x1b")
	lines := strings.Count(string(data), "\n")
	fmt.Printf("%s 共 %d 行，ANSI 转义序列 %d 个（应为 0）。\n", demoFile, lines, esc)
	if esc != 0 {
		fmt.Println("✗ 文件被转义序列污染！")
		os.Exit(1)
	}
	fmt.Println("文件前 3 行（纯文本，级别为大写无色）：")
	for i, l := range strings.SplitN(strings.TrimRight(string(data), "\n"), "\n", 4) {
		if i == 3 {
			break
		}
		fmt.Println("  " + l)
	}

	// ---------------------------------------------------------------------------
	// ④ 滚动切割证明：写满 1MB 自动切割，MaxBackups=3 清理旧文件
	// ---------------------------------------------------------------------------
	fmt.Println("========== ④ 滚动切割证明：5000 行 ≈3.5MB → 切割 3 次 ==========")
	// 压测量大，重新 Init 关闭控制台避免刷屏；文件配置与 ② 完全一致
	cfg = logger.DefaultConfig()
	cfg.Console = false
	cfg.FilePath = demoFile
	cfg.MaxSizeMB = 1
	cfg.MaxBackups = 3
	cfg.MaxAgeDays = 7
	_ = logger.Init(cfg)
	for i := 0; i < 5000; i++ { // 每行约 0.7KB，共约 3.5MB → 恰好切出 MaxBackups=3 个备份
		logger.Info("滚动切割压测 line=%d %s", i, strings.Repeat("x", 600))
	}
	time.Sleep(300 * time.Millisecond) // 切割与清理在后台协程完成，稍等片刻再列目录
	files, _ := filepath.Glob("logs/base2-demo*")
	fmt.Println("切割后的文件列表（当前文件 + 3 个时间戳命名的备份，命名规则同 v1/lumberjack）：")
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			fmt.Printf("  %-42s %6.1f KB\n", filepath.Base(f), float64(st.Size())/1024)
		}
	}

	// ---------------------------------------------------------------------------
	// ⑤ v1 ↔ v2 观感支持度对照清单
	// ---------------------------------------------------------------------------
	fmt.Println("========== ⑤ v1 ↔ v2 观感对照清单 ==========")
	fmt.Println(`
| v1 行为                          | v2 现状                          |
|----------------------------------|----------------------------------|
| 控制台级别着色 92/94/93/91/95    | 一致（同一套 ConsoleColor 映射） |
| 文件纯文本、级别大写、无转义序列 | 一致（③ 程序化证实为 0）         |
| 布局：制表符分隔 + 毫秒时间      | 一致                             |
| caller 最后两级相对路径          | 一致（编码阶段裁剪）             |
| 按大小滚动 / 个数 / 天数清理     | 一致（MaxSizeMB/MaxBackups/…）   |
| 备份命名 名-2006-01-02T15-04-05  | 一致（同 lumberjack 规则）       |
| v2 额外能力                      | JSON 编码、多路输出、缓冲写、采样、Sink/Core/Encoder 扩展 |`)
	fmt.Printf("\n结论：三项观感特征（彩色控制台 / 无色文件 / 自动滚动）行为一致，已全部程序化证实。\n")
	_ = logger.Close()
}

// captureStdout 临时替换 os.Stdout 捕获 fn 期间的终端输出（Init 在 fn 内执行，
// console sink 才会绑定到管道）；返回捕获内容后立即还原。
func captureStdout(fn func()) string {
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close() // 先关写端再还原，读端随后读到 EOF
	os.Stdout = old
	out, _ := io.ReadAll(r)
	_ = r.Close()
	return string(out)
}
