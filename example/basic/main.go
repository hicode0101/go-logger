// 示例一：基础日志使用 —— 逐项演示重构前 go-logger（v1）的全部功能在 v2 中的对等实现。
//
// 运行方式（在 example 目录下）：
//
//	go run ./basic
//
// 说明：末尾的 Fatal 演示会以退出码 1 结束进程，属预期行为；
// 日志文件生成在本目录 logs/ 下（已被主仓库 .gitignore 忽略）。
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hicode0101/go-logger"
)

func main() {
	// ---------------------------------------------------------------------------
	// ① import 即用：不调用 Init，按默认配置输出（控制台彩色 + logs/app.log 滚动文件）
	//    v1 的全部级别 API 完整保留：Debug/Info/Warn/Error/ErrorWithErr/Panic/DPanic/Fatal
	// ---------------------------------------------------------------------------
	fmt.Println("========== ① import 即用，全部级别 ==========")
	logger.Debug("Debug：开发期细节，生产环境会被级别过滤")
	logger.Info("Info：业务关键节点打点")
	logger.Warn("Warn：可自愈的异常")
	logger.Error("Error：error 及以上级别自动附带调用堆栈")
	logger.ErrorWithErr("读取配置失败", errors.New("file not found")) // v1.1 的 Error(msg, err) 约定保留

	// ---------------------------------------------------------------------------
	// ② 参数三形态：v1 签名 (f interface{}, v ...interface{}) 的完整语义
	// ---------------------------------------------------------------------------
	fmt.Println("========== ② 参数三形态（纯文本 / fmt 格式串 / 非字符串） ==========")
	logger.Info("纯文本消息")
	logger.Info("fmt 格式串：%s 的订单 %d 已支付", "Tom", 10086)
	logger.Info(errors.New("非字符串参数自动转字符串")) // error 直接传
	logger.Info(12345)                      // 数字直接传
	logger.Warnf("显式 *f 变体：磁盘使用率 %.1f%%", 85.56)

	// ---------------------------------------------------------------------------
	// ③ Panic / DPanic：先写日志再 panic，可被 recover 统一兜底
	//    DPanic 仅在 Development=true 时 panic（生产模式等同 Error）
	// ---------------------------------------------------------------------------
	fmt.Println("========== ③ Panic 与 DPanic（Development 模式） ==========")
	func() {
		defer func() { fmt.Printf("已 recover 到 Panic: %v\n", recover()) }()
		logger.Panic("Panic 演示：这条日志先落盘，然后 panic")
	}()
	cfg := logger.DefaultConfig()
	cfg.Development = true // 开发模式：DPanic 直接 panic
	_ = logger.Init(cfg)
	func() {
		defer func() { fmt.Printf("已 recover 到 DPanic: %v\n", recover()) }()
		logger.DPanic("DPanic 演示：Development=true 时 panic，生产模式等同 Error")
	}()

	// ---------------------------------------------------------------------------
	// ④ 子日志器 Named / 上下文字段 WithFields / 实例方法
	// ---------------------------------------------------------------------------
	fmt.Println("========== ④ Named 子日志器与 WithFields 上下文 ==========")
	httpLog := logger.Named("http")
	dbLog := logger.Named("db")
	httpLog.Info("listening on :8080") // 输出中带 logger 名称 http
	dbLog.Info("query users")

	reqLog := logger.WithFields(map[string]interface{}{ // v1 返回 *zap.Logger，v2 返回 *logger.Logger
		"requestId": "req-9f8e7d",
		"uid":       10001,
	})
	reqLog.Info("开始处理下单") // 每条自动携带 requestId/uid
	logger.GetLogger().Warn("GetLogger 实例方法与全局函数行为一致")

	// ---------------------------------------------------------------------------
	// ⑤ 运行期动态级别：SetLevel 原子生效，排查问题不重启
	// ---------------------------------------------------------------------------
	fmt.Println("========== ⑤ 动态级别 SetLevel / GetLevel / Enabled ==========")
	logger.Debug("此时级别为 debug，这条会输出")
	_ = logger.SetLevel("warn")
	logger.Debug("级别已切到 warn，这条被过滤，零成本返回")
	logger.Warn("这条 warn 会输出")
	fmt.Printf("当前级别: %s（GetLevel 返回 logger.Level，可与常量比较）\n", logger.GetLevel())
	if !logger.GetLogger().Enabled(logger.DebugLevel) {
		fmt.Println("Enabled 预判为 false：可在调用前短路昂贵的参数构造")
	}
	_ = logger.SetLevel("info")

	// ---------------------------------------------------------------------------
	// ⑥ 滚动切割（lumberjack 等价）：按大小切割、按个数清理、可选 gzip 压缩
	// ---------------------------------------------------------------------------
	fmt.Println("========== ⑥ 滚动切割：写满 1MB 自动切割，MaxBackups=3 ==========")
	cfg = logger.DefaultConfig()
	cfg.Console = false // 切割演示关闭控制台，避免刷屏
	cfg.FilePath = "logs/rotate-demo.log"
	cfg.MaxSizeMB = 1  // 单文件 1MB 上限
	cfg.MaxBackups = 3 // 最多保留 3 个历史文件
	cfg.MaxAgeDays = 7
	cfg.Compress = false // 置 true 时历史文件自动 gzip
	_ = logger.Init(cfg)
	for i := 0; i < 5000; i++ { // 每行约 0.7KB，共约 3.5MB → 切割 3 次 → 恰好保留 MaxBackups=3 个备份
		logger.Info("滚动切割压测 line=%d %s", i, strings.Repeat("x", 600))
	}
	time.Sleep(300 * time.Millisecond) // 切割与清理在后台协程完成，稍等片刻再列目录
	files, _ := filepath.Glob("logs/rotate-demo*")
	fmt.Println("切割后的文件列表（当前文件 + 3 个按时间戳命名的备份）：")
	for _, f := range files {
		fmt.Println("  " + filepath.Base(f))
	}

	// ---------------------------------------------------------------------------
	// ⑦ v2 新能力速览：结构化字段、JSON 编码、多路输出、缓冲写
	// ---------------------------------------------------------------------------
	fmt.Println("========== ⑦ 新能力速览：多路输出 + JSON 编码 + 缓冲写 ==========")
	_ = logger.Init(logger.Config{
		Level: "info",
		Sinks: []logger.SinkConfig{
			{Type: "console", Format: "console", Color: true, Level: "warn"}, // 屏幕只看 warn+
			{Type: "file", Format: "json", Path: "logs/basic-json.log",
				MaxSizeMB: 10, BufferSizeKB: 64}, // 文件 JSON 全量 + 缓冲写
		},
	})
	logger.GetLogger().Info("这条 Info 只进文件，不上屏（console sink 级别为 warn）")
	logger.Errorw("下单失败", "orderNo", "NO20240101", "amount", 99.5) // kv 键值对
	logger.GetLogger().Warn("强类型字段",                               // 零装箱分配
		logger.Int("uid", 10001),
		logger.Float64("amount", 99.5),
		logger.Duration("cost", 350*time.Millisecond),
	)
	_ = logger.Sync() // 缓冲写模式下 Sync 立即刷盘，之后即可读到文件内容
	if data, err := os.ReadFile("logs/basic-json.log"); err == nil {
		fmt.Println("logs/basic-json.log 内容（JSON 行，可直接对接采集管道）：")
		fmt.Println(string(data))
	}

	// ---------------------------------------------------------------------------
	// ⑧ Fatal：写日志 → Sync → os.Exit(1)，演示到此结束（退出码 1 属预期）
	// ---------------------------------------------------------------------------
	fmt.Println("========== ⑧ Fatal：写日志后进程退出（退出码 1 属预期） ==========")
	logger.Fatal("端口监听失败演示") // 前面的 defer 不会执行，Fatal 内部已自动 Sync
}
