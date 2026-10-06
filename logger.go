// Package logger 一个基于 zap + lumberjack 的开箱即用日志库。
//
// 设计目标（软件重用）：
//   - 开箱即用：import 后直接调用 Debug/Info/Warn/Error 即可输出日志（控制台 + 滚动文件）；
//   - 可配置：通过 Init(Config) 自定义级别、文件路径、滚动策略，可重复调用重建；
//   - 低依赖：仅依赖两个精心挑选的第三方库——zap（高性能结构化日志）与
//     lumberjack（按大小/时间滚动切割日志文件），不再引入其它依赖。
//
// 基本用法：
//
//	import "github.com/hicode0101/go-logger"
//
//	func main() {
//		logger.Info("用户 %s 登录成功", "Tom")
//		logger.ErrorWithErr("读取配置失败", err)
//		defer logger.Close()
//	}
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Config 日志初始化配置。
// 字段留空（零值）时使用对应默认值，见 DefaultConfig。
type Config struct {
	Level       string // 日志级别：debug/info/warn/error/dpanic/panic/fatal（大小写均可），默认 debug
	Console     bool   // 是否输出到控制台，默认 true
	File        bool   // 是否写入滚动日志文件，默认 true
	FilePath    string // 日志文件路径，默认 logs/app.log
	MaxSizeMB   int    // 单个日志文件最大体积（MB），超过后自动切割，默认 1024
	MaxBackups  int    // 最多保留的历史日志文件个数，默认 10
	MaxAgeDays  int    // 历史日志最长保留天数，默认 7
	Compress    bool   // 是否用 gzip 压缩历史日志文件，默认 false
	Development bool   // 开发模式：DPanic 级别直接 panic，默认 false
}

// DefaultConfig 返回一份默认配置，可直接修改个别字段后传给 Init。
func DefaultConfig() Config {
	return Config{
		Level:      "debug",
		Console:    true,
		File:       true,
		FilePath:   filepath.Join("logs", "app.log"),
		MaxSizeMB:  1024,
		MaxBackups: 10,
		MaxAgeDays: 7,
	}
}

var (
	// std 全局默认 logger。
	// 包初始化时按默认配置创建；Init 可整体替换。
	// 以 Nop 兜底，保证任何时刻调用日志函数都不会因未初始化而 panic。
	std = zap.NewNop()

	// stdLevel 全局动态级别，控制台与文件两个输出核心共用同一实例，
	// 因此 SetLevel 在运行期修改级别时对两边同时生效。
	stdLevel = zap.NewAtomicLevelAt(zapcore.DebugLevel)

	// fileWriter 当前使用的滚动文件写入器。
	// Init 重建 logger 或 Close 时用它释放文件句柄。
	fileWriter *lumberjack.Logger
)

func init() {
	// 默认配置中的级别固定合法，初始化不会失败，忽略错误即可
	_ = Init(DefaultConfig())
}

// Init 按配置重建全局 logger，可重复调用以切换配置或输出目标。
// 重建前会先刷写并关闭旧日志文件句柄，避免句柄泄漏。
// 返回错误通常只发生在 Level 取值非法时。
func Init(cfg Config) error {
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		return fmt.Errorf("logger: 无效的日志级别 %q: %w", cfg.Level, err)
	}
	stdLevel.SetLevel(level)

	// 控制台与文件共用一套字段编码，仅级别编码不同：控制台带颜色，文件用纯大写文本
	consoleCfg := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    ColorLevelEncoder,
		EncodeTime:     TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}
	fileCfg := consoleCfg
	fileCfg.EncodeLevel = zapcore.CapitalLevelEncoder

	cores := make([]zapcore.Core, 0, 2)
	var newWriter *lumberjack.Logger

	if cfg.Console {
		cores = append(cores, zapcore.NewCore(
			zapcore.NewConsoleEncoder(consoleCfg),
			zapcore.Lock(os.Stdout), // Lock 保证并发写 stdout 安全
			&stdLevel,
		))
	}

	if cfg.File {
		path := cfg.FilePath
		if path == "" {
			path = DefaultConfig().FilePath
		}
		newWriter = &lumberjack.Logger{
			Filename:   path,
			MaxSize:    defaultInt(cfg.MaxSizeMB, 1024), // 单位 MB
			MaxBackups: defaultInt(cfg.MaxBackups, 10),
			MaxAge:     defaultInt(cfg.MaxAgeDays, 7), // 单位天
			Compress:   cfg.Compress,
			LocalTime:  true, // 按本地时间命名切割文件
		}
		cores = append(cores, zapcore.NewCore(
			zapcore.NewConsoleEncoder(fileCfg),
			zapcore.AddSync(newWriter), // lumberjack 未实现 Sync，用 AddSync 补一个空实现
			&stdLevel,
		))
	}

	// 换配置前释放旧文件句柄
	_ = closeOldWriter()
	fileWriter = newWriter

	// 控制台与文件全关时退化为 Nop logger，语义清晰且零开销
	if len(cores) == 0 {
		std = zap.NewNop()
		return nil
	}

	opts := []zap.Option{
		zap.AddCaller(),
		// Debug/Info 等包装函数比业务代码多一层调用栈，回溯一层才能定位到真实调用点
		zap.AddCallerSkip(1),
		// error 及以上级别自动附带调用堆栈
		zap.AddStacktrace(zapcore.ErrorLevel),
	}
	if cfg.Development {
		opts = append(opts, zap.Development())
	}

	std = zap.New(zapcore.NewTee(cores...), opts...)
	return nil
}

// SetLevel 运行期动态调整日志级别，立即对控制台和文件同时生效。
// 取值：debug、info、warn、error、dpanic、panic、fatal（大小写均可）。
func SetLevel(level string) error {
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		return fmt.Errorf("logger: 无效的日志级别 %q: %w", level, err)
	}
	stdLevel.SetLevel(lvl)
	return nil
}

// GetLevel 返回当前日志级别。
func GetLevel() zapcore.Level {
	return stdLevel.Level()
}

// Debug 输出调试日志。
// f 支持三种形态：纯文本；带 % 的 fmt 格式串（v 为参数）；非字符串值（自动转字符串）。
func Debug(f interface{}, v ...interface{}) {
	std.Debug(formatMessage(f, v...))
}

// Info 输出一般信息日志，格式规则同 Debug。
func Info(f interface{}, v ...interface{}) {
	std.Info(formatMessage(f, v...))
}

// Warn 输出警告日志，格式规则同 Debug。
func Warn(f interface{}, v ...interface{}) {
	std.Warn(formatMessage(f, v...))
}

// Error 输出错误日志（error 及以上级别自动附带调用堆栈），格式规则同 Debug。
func Error(f interface{}, v ...interface{}) {
	std.Error(formatMessage(f, v...))
}

// ErrorWithErr 输出携带 error 字段的错误日志。
// 适合"发生了什么事 + 原始错误"的典型场景，err 会以独立字段记录，方便日志采集检索。
func ErrorWithErr(msg string, err error) {
	if err == nil {
		std.Error(msg)
		return
	}
	std.Error(msg, zap.Error(err))
}

// Panic 输出日志后执行 panic()，适合不可恢复且希望 recover 捕获的场景。
func Panic(f interface{}, v ...interface{}) {
	std.Panic(formatMessage(f, v...))
}

// Fatal 输出日志后执行 os.Exit(1)，适合启动阶段的致命错误。
// 注意：进程直接退出，defer 不会执行；退出前请视需要自行调用 Sync。
func Fatal(f interface{}, v ...interface{}) {
	std.Fatal(formatMessage(f, v...))
}

// Named 返回带子名称的 logger，日志的 logger 字段会显示该名称，
// 适合按模块区分日志，例如 logger.Named("http")。
func Named(name string) *zap.Logger {
	return std.Named(name)
}

// WithFields 返回携带固定业务字段的 logger，这些字段出现在该 logger 的每条日志中。
// 常用于注入请求 ID、用户 ID 等上下文信息。
func WithFields(fields map[string]interface{}) *zap.Logger {
	zapFields := make([]zap.Field, 0, len(fields))
	for k, v := range fields {
		zapFields = append(zapFields, zap.Any(k, v))
	}
	return std.With(zapFields...)
}

// GetLogger 返回底层 *zap.Logger，便于使用 zap 的高级能力
// （SugaredLogger、Hooks、自定义 Field 等）。
// 返回副本已抵消包装函数引入的一层 caller 偏移，直接使用时行号定位是准确的。
func GetLogger() *zap.Logger {
	return std.WithOptions(zap.AddCallerSkip(-1))
}

// Sync 刷写所有缓冲日志，进程退出前建议调用一次。
func Sync() error {
	return std.Sync()
}

// Close 刷写并关闭日志文件句柄（仅控制台输出时为空操作），
// 进程优雅退出或重建配置前调用。
func Close() error {
	return closeOldWriter()
}

// formatMessage 统一格式化日志消息，规则：
//   - f 为字符串且含 %、有参数：按 fmt 格式串处理；
//   - f 为字符串、无参数：原样输出；
//   - f 为非字符串（error、int 等）：先 fmt.Sprint 转字符串；
//   - 有参数但消息不含 %：自动为每个参数追加一个 %v 占位。
func formatMessage(f interface{}, v ...interface{}) string {
	msg, isStr := f.(string)
	if !isStr {
		msg = fmt.Sprint(f)
	}
	if len(v) == 0 {
		return msg
	}
	if strings.Contains(msg, "%") {
		return fmt.Sprintf(msg, v...)
	}
	return fmt.Sprintf(msg+strings.Repeat(" %v", len(v)), v...)
}

// closeOldWriter 刷写并关闭当前日志文件句柄，无文件输出时为空操作。
func closeOldWriter() error {
	if fileWriter == nil {
		return nil
	}
	err := fileWriter.Close()
	fileWriter = nil
	return err
}

// defaultInt 配置零值兜底：v 非正时返回默认值 def。
func defaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
