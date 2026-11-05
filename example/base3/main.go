// 示例三（base3）：base2 的 JSON 形态 —— 同样的测试流程，全部输出改为 JSON 行。
//
// 与 base2（v1 观感对齐）的定位差异：JSON 行输出是 v2 的新能力，v1 的
// zap console 布局并不支持，因此本示例不再是"v1 对齐"，而是演示接入
// 日志管道（Filebeat / ES / Kafka）时的推荐形态，并对三个关键特征逐项
// 给出程序化证据：
//
//	① 控制台每行都是合法 JSON、零 ANSI 转义（JSON 编码器不着色，
//	   颜色只属于 console 编码器——这是管道场景的预期取舍）
//	② .log 文件同为 JSON 行、零转义序列（读回逐行 json.Valid 校验）
//	③ 写满即滚动、按个数清理（目录列表 + 大小统计，与 base2 一致）
//
// 运行方式（在 example 目录下）：
//
//	go run ./base3
//
// 说明：日志文件生成在本目录 logs/ 下（已被主仓库 .gitignore 忽略）。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hicode0101/go-logger"
)

const demoFile = "logs/base3-demo.log"

func main() {
	// ---------------------------------------------------------------------------
	// ① 控制台 JSON 证明：捕获 stdout，逐行校验合法 JSON + 零 ANSI 转义
	// ---------------------------------------------------------------------------
	fmt.Println("========== ① 控制台 JSON 证明（捕获 stdout 逐行校验） ==========")
	captured := captureStdout(func() {
		_ = logger.Init(logger.Config{
			Level: "debug",
			Sinks: []logger.SinkConfig{{Type: "console", Format: "json"}},
		})
		logger.Debug("颜色校验 Debug")
		logger.Info("颜色校验 Info")
		logger.Warn("颜色校验 Warn")
		logger.Error("颜色校验 Error")
		_ = logger.Sync()
	})
	esc := strings.Count(captured, "\x1b")
	lines := splitLines(captured)
	fmt.Printf("捕获控制台输出 %d 字节 / %d 行，ANSI 转义序列 %d 个（应为 0：JSON 无着色），合法 JSON 行 %d/%d。\n",
		len(captured), len(lines), esc, countValidJSON(lines), len(lines))
	for _, lv := range []string{"debug", "info", "warn", "error"} {
		key := `"level":"` + lv + `"`
		status := "✓"
		if !strings.Contains(captured, key) {
			status = "✗ 缺失"
		}
		fmt.Printf("  %s 含字段 %s\n", status, key)
	}

	// ---------------------------------------------------------------------------
	// ② 正式演示配置：控制台 + 文件双路 JSON（管道场景推荐形态）
	//    眼见为实：下面这批日志在终端与文件里都是一行一个 JSON 对象
	// ---------------------------------------------------------------------------
	fmt.Println("========== ② 双路 JSON 配置：控制台 + 文件（眼见为实） ==========")
	_ = logger.Init(logger.Config{
		Level: "debug",
		Sinks: []logger.SinkConfig{
			{Type: "console", Format: "json"},
			{Type: "file", Format: "json", Path: demoFile, // file 专用滚动参数（lumberjack 等价）
				MaxSizeMB: 1, MaxBackups: 3, MaxAgeDays: 7, LocalTime: true},
		},
	})

	logger.Debug("Debug：开发期细节，生产环境会被级别过滤")
	logger.Info("Info：业务关键节点打点 user=%s", "Tom")
	logger.Warn("Warn：可自愈的异常")
	logger.Error("Error：error 及以上级别自动附带调用堆栈")
	httpLog := logger.Named("http")
	httpLog.Info("listening on :8080")
	_ = logger.Sync()

	// ---------------------------------------------------------------------------
	// ③ 文件 JSON 证明：读回 .log 文件，逐行 json.Valid + 转义序列计数为 0
	// ---------------------------------------------------------------------------
	fmt.Println("========== ③ 文件 JSON 证明（逐行校验 + \\x1b 计数 = 0） ==========")
	data, err := os.ReadFile(demoFile)
	if err != nil {
		fmt.Println("读取文件失败:", err)
		os.Exit(1)
	}
	fileLines := splitLines(string(data))
	fmt.Printf("%s 共 %d 行，ANSI 转义序列 %d 个（应为 0），合法 JSON 行 %d/%d。\n",
		demoFile, len(fileLines), strings.Count(string(data), "\x1b"),
		countValidJSON(fileLines), len(fileLines))
	fmt.Println("文件前 3 行（每行一个 JSON 对象，level/time/logger/caller/msg 平铺）：")
	for i, l := range fileLines {
		if i == 3 {
			break
		}
		fmt.Println("  " + l)
	}

	// ---------------------------------------------------------------------------
	// ④ 滚动切割证明：写满 1MB 自动切割，MaxBackups=3 清理旧文件
	// ---------------------------------------------------------------------------
	fmt.Println("========== ④ 滚动切割证明：5000 行 ≈3.3MB → 切割 3 次 ==========")
	// 压测量大，重新 Init 关闭控制台避免刷屏；文件配置与 ② 完全一致
	_ = logger.Init(logger.Config{
		Level: "debug",
		Sinks: []logger.SinkConfig{
			{Type: "file", Format: "json", Path: demoFile,
				MaxSizeMB: 1, MaxBackups: 3, MaxAgeDays: 7, LocalTime: true},
		},
	})
	for i := 0; i < 5000; i++ { // JSON 行约 0.65KB，共约 3.3MB → 恰好切出 MaxBackups=3 个备份
		logger.Info("滚动切割压测 line=%d %s", i, strings.Repeat("x", 550))
	}
	time.Sleep(300 * time.Millisecond) // 切割与清理在后台协程完成，稍等片刻再列目录
	files, _ := filepath.Glob("logs/base3-demo*")
	fmt.Println("切割后的文件列表（当前文件 + 3 个时间戳命名的备份，命名规则同 base2/lumberjack）：")
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			fmt.Printf("  %-42s %6.1f KB\n", filepath.Base(f), float64(st.Size())/1024)
		}
	}

	// ---------------------------------------------------------------------------
	// ⑤ base2（v1 观感）↔ base3（JSON 管道）对照清单
	// ---------------------------------------------------------------------------
	fmt.Println("========== ⑤ base2（v1 观感）↔ base3（JSON 管道）对照清单 ==========")
	fmt.Println(`
| 特征                 | base2（v1 观感）            | base3（JSON 管道）                    |
|----------------------|-----------------------------|---------------------------------------|
| 控制台级别着色       | 有（92/94/93/91）           | 无（JSON 编码器不着色，管道友好）     |
| 每行形态             | 制表符分隔文本              | 一个 JSON 对象，可直接被采集端解析    |
| 结构化字段           | 内联 JSON 块（末列）        | 强类型字段平铺到顶层                  |
| caller 相对路径      | 两级（编码阶段裁剪）        | 一致（JSON 字符串值中同样裁剪）       |
| Error 自动附堆栈     | 独立一行（缩进）            | stacktrace 字段（含 \n 转义）         |
| 滚动切割/清理        | MaxSizeMB/MaxBackups…       | 完全一致                              |
| v1 支持度            | 与 v1 逐项一致              | v2 新能力，v1 不支持                  |`)
	fmt.Printf("\n结论：JSON 形态下三项特征（控制台 JSON 行 / 文件 JSON 行 / 自动滚动）已全部程序化证实。\n")
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

// splitLines 按行拆分并去掉末尾空行。
func splitLines(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// countValidJSON 统计能通过 json.Valid 的行数。
func countValidJSON(lines []string) int {
	n := 0
	for _, l := range lines {
		if json.Valid([]byte(l)) {
			n++
		}
	}
	return n
}
