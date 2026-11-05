// Package logger 一个零第三方依赖、开箱即用、高性能的 Go 日志库。
//
// 本包以 Go 标准库完整重实现了 zap 的核心能力（缓冲池编码、类型化字段、
// 动态级别、采样、调用位置/堆栈采集）与 lumberjack 的日志滚动切割能力，
// 并抽象出 Encoder / Core / Sink 三层扩展接口，支持按同一套 API 接入
// 本地文件、Elasticsearch、Kafka、StarRocks、MinIO 等多种日志目的地
// （内置先实现本地文件与控制台，远程目的地见 docs/EXTEND.md 扩展指南）。
//
// 基本用法（import 即用，无需初始化）：
//
//	import "github.com/hicode0101/go-logger"
//
//	func main() {
//		logger.Info("用户 %s 登录成功", "Tom")            // 格式串风格
//		logger.ErrorWithErr("读取配置失败", err)           // 携带 error 字段
//		logger.Infow("下单成功", "orderNo", "NO1")        // 结构化键值对
//		defer logger.Close()                             // 进程退出前释放句柄
//	}
//
// 性能设计要点（详见 docs/DESIGN.md）：
//   - 日志级别一次原子读过滤，禁用级别零分配直接返回；
//   - 编码直写 sync.Pool 复用的字节缓冲，无 fmt/反射路径；
//   - Field 强类型构造函数避免 interface{} 装箱；
//   - 时间、JSON 转义、级别名全部手工/查表编码；
//   - 滚动切割与清理在后台协程完成，写入路径零阻塞。
package logger

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// callerSkipOffset Logger 方法链到业务代码的固定帧偏移：
// 业务代码 → Logger.Info / 包级 Debug → log.log → getCaller。
// 已按 runtime.Callers 语义校准，正常使用无需关心。
const callerSkipOffset = 2

// Logger 日志器实例。全局默认实例 std 在首次日志调用时懒创建，
// Init 可整体重建；Named/With 返回共享底层核心、携带不同上下文的子实例。
type Logger struct {
	core        Core      // 处理链核心（含上下文字段）
	name        string    // 子日志器名（Named 逐级以 . 连接）
	callerSkip  int       // 额外调用帧偏移（包装本库的库用）
	addCaller   bool      // 是否记录调用位置
	development bool      // 开发模式：DPanic 直接 panic
	stackLevel  Level     // 该级别及以上采集堆栈
	exitFunc    func(int) // Fatal 退出函数，测试可替换
}

// ---------------------------------------------------------------------------
// 全局默认实例
// ---------------------------------------------------------------------------

var (
	// std 全局默认日志器，首次日志调用时按默认配置懒创建，Init 可整体替换。
	std atomic.Pointer[Logger]
)

// defaultInitOnce 懒初始化守卫：仅当全局日志器尚未就绪时执行一次默认 Init。
var defaultInitOnce sync.Once

// stdLogger 取当前全局日志器（Init 替换后自动生效）。
//
// 默认日志器采用"首次使用时懒初始化"而非包级 init()：Go 按文件名字母序
// 执行包内 init，本文件先于 sink.go 执行，此时 console/file Sink 工厂
// 尚未注册，init 里调 Init 必然失败且错误无处上报——包级 API 会整体静默。
// 懒初始化时所有包内 init 已完成、注册表就绪，且显式 Init 失败（如传入了
// 非法级别）后未恢复时也能在此兜底回默认行为。
func stdLogger() *Logger {
	if l := std.Load(); l != nil {
		return l
	}
	defaultInitOnce.Do(func() {
		if std.Load() == nil { // 其他路径已成功初始化则不重建，避免多余开关文件
			_ = Init(DefaultConfig())
		}
	})
	if l := std.Load(); l != nil {
		return l
	}
	// 默认配置级别合法，懒初始化不会失败；防御性兜底为静默日志器
	return &Logger{core: nopCore{}}
}

// Init 按配置重建全局日志器，可重复调用以切换配置或输出目标。
// 重建前先刷写并关闭旧输出句柄，避免句柄泄漏。
// 返回错误通常只发生在 Level 取值非法时。
func Init(cfg Config) error {
	level, err := ParseLevel(cfg.Level)
	if err != nil {
		return err
	}
	sharedLevel := NewAtomicLevel(level)

	// 换配置前先释放旧输出句柄（旧日志器随即不可用，新的构建成功后原子替换）。
	// 直接检查 std 而不走 stdLogger()：stdLogger 会触发懒初始化，若在
	// defaultInitOnce.Do 内部递归回来会死锁，且首个显式 Init 会多建一次默认实例。
	if l := std.Load(); l != nil {
		_ = l.Close()
	}

	var cores []Core
	if len(cfg.Sinks) > 0 {
		// 新版多路输出模式：完全按 SinkConfig 列表构建
		for i := range cfg.Sinks {
			sc := cfg.Sinks[i]
			core, err := buildSinkCore(sc, sharedLevel)
			if err != nil {
				return err
			}
			cores = append(cores, core)
		}
	} else {
		// 兼容旧版：Console + File 两个开关构建
		if cfg.Console {
			sc := SinkConfig{Type: "console", Format: "console", Color: cfg.ConsoleColor}
			core, err := buildSinkCore(sc, sharedLevel)
			if err != nil {
				return err
			}
			cores = append(cores, core)
		}
		if cfg.File {
			sc := SinkConfig{
				Type:             "file",
				Format:           cfg.Encoding,
				Path:             cfg.FilePath,
				MaxSizeMB:        cfg.MaxSizeMB,
				MaxBackups:       cfg.MaxBackups,
				MaxAgeDays:       cfg.MaxAgeDays,
				Compress:         cfg.Compress,
				LocalTime:        true,
				Color:            false,
				BufferSizeKB:     cfg.FileBufferSizeKB,
				FlushIntervalSec: cfg.FlushIntervalSec,
			}
			if sc.Path == "" {
				sc.Path = DefaultConfig().FilePath
			}
			core, err := buildSinkCore(sc, sharedLevel)
			if err != nil {
				return err
			}
			cores = append(cores, core)
		}
	}

	var top Core = NewTee(cores...)
	if cfg.Sampling != nil {
		top = newSamplerCore(top, *cfg.Sampling, nil)
	}

	l := &Logger{
		core:        top,
		addCaller:   !cfg.DisableCaller,
		development: cfg.Development,
		exitFunc:    os.Exit,
	}
	if cfg.DisableStacktrace {
		l.stackLevel = FatalLevel + 1 // 永不采集
	} else {
		l.stackLevel = ErrorLevel
	}
	std.Store(l)
	return nil
}

// NewLogger 用自定义 Core 构造日志器实例——结构化目的地扩展点。
//
// Elasticsearch / StarRocks 等按文档行消费日志的存储，推荐实现 Core 接口
// 直接消费结构化 Entry + Field（见 docs/EXTEND.md），经本函数包装后即可
// 使用与全局 logger 完全一致的实例方法：
//
//	esLog := logger.NewLogger(myESCore)
//	esLog.Info("indexed", logger.String("index", "logs-2025.11.05"))
//
// 默认行为与 Init 的默认配置一致：记录 caller、error 及以上级别采集堆栈、
// Fatal 后 os.Exit(1)。实例与全局日志器相互独立；全局实例无法注入自定义
// Core，需要所有日志进自定义目的地时改用 RegisterSink + Sinks 配置
// （字节流目的地）或把本实例作为业务专用日志器。
func NewLogger(core Core) *Logger {
	return &Logger{
		core:       core,
		addCaller:  true,
		stackLevel: ErrorLevel,
		exitFunc:   os.Exit,
	}
}

// buildSinkCore 构建单个输出目标的核心。
func buildSinkCore(sc SinkConfig, level *AtomicLevel) (Core, error) {
	sink, err := newSink(sc)
	if err != nil {
		return nil, err
	}
	enc := newEncoderByName(sc.Format, EncoderConfig{Color: sc.Color && sc.Format == "console"})

	coreLevel := LevelEnabler(level)
	if sc.Level != "" {
		// 每路输出可配置独立级别，取与全局级别的较高者
		indep, err := ParseLevel(sc.Level)
		if err != nil {
			return nil, err
		}
		coreLevel = combinedLevel{a: indep, b: level}
	}
	return NewCore(coreLevel, enc, sink), nil
}

// combinedLevel 组合级别开关：独立级别与全局级别都通过才启用。
type combinedLevel struct {
	a Level
	b *AtomicLevel
}

func (c combinedLevel) Enabled(lv Level) bool { return lv >= c.a && c.b.Enabled(lv) }

// ---------------------------------------------------------------------------
// Logger 实例方法（结构化 API）
// ---------------------------------------------------------------------------

// Debug 输出调试日志。
func (log *Logger) Debug(msg string, fields ...Field) { log.log(DebugLevel, msg, fields) }

// Info 输出一般信息日志。
func (log *Logger) Info(msg string, fields ...Field) { log.log(InfoLevel, msg, fields) }

// Warn 输出警告日志。
func (log *Logger) Warn(msg string, fields ...Field) { log.log(WarnLevel, msg, fields) }

// Error 输出错误日志（error 及以上自动附带堆栈）。
func (log *Logger) Error(msg string, fields ...Field) { log.log(ErrorLevel, msg, fields) }

// DPanic 开发模式输出后 panic，生产模式等同 Error。
func (log *Logger) DPanic(msg string, fields ...Field) { log.log(DPanicLevel, msg, fields) }

// Panic 输出日志后 panic，适合希望被上层 recover 统一兜底的场景。
func (log *Logger) Panic(msg string, fields ...Field) { log.log(PanicLevel, msg, fields) }

// Fatal 输出日志后 os.Exit(1)；进程直接退出，defer 不执行。
func (log *Logger) Fatal(msg string, fields ...Field) { log.log(FatalLevel, msg, fields) }

// Debugf 按 fmt 格式串输出调试日志。
func (log *Logger) Debugf(format string, args ...interface{}) {
	log.log(DebugLevel, formatMessage(format, args...), nil)
}

// Infof 按 fmt 格式串输出信息日志。
func (log *Logger) Infof(format string, args ...interface{}) {
	log.log(InfoLevel, formatMessage(format, args...), nil)
}

// Warnf 按 fmt 格式串输出警告日志。
func (log *Logger) Warnf(format string, args ...interface{}) {
	log.log(WarnLevel, formatMessage(format, args...), nil)
}

// Errorf 按 fmt 格式串输出错误日志。
func (log *Logger) Errorf(format string, args ...interface{}) {
	log.log(ErrorLevel, formatMessage(format, args...), nil)
}

// *w 系列方法：结构化键值接口。kv 须成对传入（key, value, key, value...），
// 奇数个时末项按 !BADKEY 记录。

// Fatalw 输出携带键值字段的致命日志（写日志 → Sync → os.Exit(1)）。
func (log *Logger) Fatalw(msg string, kv ...interface{}) {
	log.log(FatalLevel, msg, kvToFields(kv))
}

// Panicw 输出携带键值字段并触发 panic。
func (log *Logger) Panicw(msg string, kv ...interface{}) {
	log.log(PanicLevel, msg, kvToFields(kv))
}

// DPanicw 输出携带键值字段的 DPanic 日志（开发模式 panic）。
func (log *Logger) DPanicw(msg string, kv ...interface{}) {
	log.log(DPanicLevel, msg, kvToFields(kv))
}

// Debugw 输出携带键值字段的调试日志。
func (log *Logger) Debugw(msg string, kv ...interface{}) {
	log.log(DebugLevel, msg, kvToFields(kv))
}

// Infow 输出携带键值字段的信息日志。
func (log *Logger) Infow(msg string, kv ...interface{}) {
	log.log(InfoLevel, msg, kvToFields(kv))
}

// Warnw 输出携带键值字段的警告日志。
func (log *Logger) Warnw(msg string, kv ...interface{}) {
	log.log(WarnLevel, msg, kvToFields(kv))
}

// Errorw 输出携带键值字段的错误日志。
func (log *Logger) Errorw(msg string, kv ...interface{}) {
	log.log(ErrorLevel, msg, kvToFields(kv))
}

// entryPool 条目对象池：&entry 经接口调用会逃逸到堆，池化复用消除
// 每条日志一次 112B 的堆分配（zap CheckedEntry 池化同款设计）。
// 生命周期约定：core.Write 返回后 entry 立即回收复用，
// 因此 Core 实现与 Hook 回调不得跨调用持有 *Entry 引用；
// 需要异步处理时必须先拷贝所需数据。
var entryPool = sync.Pool{
	New: func() interface{} { return new(Entry) },
}

// log 日志主热路径。skip 为 getCaller/takeStacktrace 的帧偏移
// （2 = 业务代码调用点；本方法内固定使用 callerSkipOffset）。
//
// 性能注释（每一步都为热路径设计）：
//  1. 级别过滤是一次原子读——禁用级别零分配零系统调用返回；
//  2. time.Now 只在级别启用后调用；
//  3. caller 只在 addCaller 时采集，堆栈只在 stackLevel 及以上采集；
//  4. Entry 池化复用，热路径零堆分配；
//  5. fields 不发生复制：With 字段已沉淀在 core 内，调用点字段直通编码。
func (log *Logger) log(lvl Level, msg string, fields []Field) {
	// 1. 最快路径：级别过滤（原子读），99% 的禁用日志在此直接返回
	if !log.core.Enabled(lvl) {
		return
	}

	entry := entryPool.Get().(*Entry)
	*entry = Entry{
		Level:      lvl,
		Time:       time.Now(),
		LoggerName: log.name,
		Message:    msg,
	}
	if log.addCaller {
		entry.CallerFile, entry.CallerLine, entry.CallerDefined = getCaller(callerSkipOffset + log.callerSkip)
	}
	if lvl >= log.stackLevel {
		entry.Stack = takeStacktrace(callerSkipOffset + log.callerSkip)
	}

	// 2. 写出（含编码）
	err := log.core.Write(entry, fields)

	// 3. 级别副作用：在回收条目前捕获 panic 值（Entry 即将被复用，须拷贝）
	var panicVal interface{}
	switch lvl {
	case DPanicLevel:
		if log.development {
			panicVal = *entry
		}
	case PanicLevel:
		panicVal = *entry
	}

	// 4. 回收条目（约定：Core/Hook 不得跨调用持有 *Entry）
	*entry = Entry{}
	entryPool.Put(entry)

	if err != nil {
		// 写出失败兜底上报 stderr，避免日志静默丢失
		fmt.Fprintf(os.Stderr, "logger: 写出日志失败: %v\n", err)
	}

	// 5. 触发副作用（日志已落盘后）
	if panicVal != nil {
		panic(panicVal)
	}
	if lvl == FatalLevel {
		_ = log.Sync()
		log.exitFunc(1)
	}
}

// Named 返回带子名称的日志器，日志的 logger 字段显示该名称，
// 多级调用以 "." 连接（Named("a").Named("b") → "a.b"）。
func (log *Logger) Named(name string) *Logger {
	child := *log
	if log.name == "" {
		child.name = name
	} else {
		child.name = log.name + "." + name
	}
	return &child
}

// With 返回携带固定结构化字段的日志器，字段出现在后续每条日志中。
func (log *Logger) With(fields ...Field) *Logger {
	child := *log
	child.core = log.core.With(fields)
	return &child
}

// Enabled 判断本日志器在 lv 级别是否会实际输出，
// 用于"先判断再构建昂贵上下文"的预判优化。
func (log *Logger) Enabled(lv Level) bool { return log.core.Enabled(lv) }

// Sync 刷写所有输出缓冲。
func (log *Logger) Sync() error { return log.core.Sync() }

// Close 刷写并关闭全部输出句柄；关闭后本日志器不可再使用。
func (log *Logger) Close() error { return log.core.Close() }

// ---------------------------------------------------------------------------
// 包级兼容 API（旧版签名原样保留）
// ---------------------------------------------------------------------------

// Debug 输出调试日志。
// f 支持三种形态：纯文本；带 % 的 fmt 格式串（v 为参数）；非字符串值（自动转字符串）。
func Debug(f interface{}, v ...interface{}) {
	stdLogger().log(DebugLevel, formatMessage(f, v...), nil)
}

// Info 输出一般信息日志，格式规则同 Debug。
func Info(f interface{}, v ...interface{}) {
	stdLogger().log(InfoLevel, formatMessage(f, v...), nil)
}

// Warn 输出警告日志，格式规则同 Debug。
func Warn(f interface{}, v ...interface{}) {
	stdLogger().log(WarnLevel, formatMessage(f, v...), nil)
}

// Error 输出错误日志（error 及以上级别自动附带调用堆栈），格式规则同 Debug。
func Error(f interface{}, v ...interface{}) {
	stdLogger().log(ErrorLevel, formatMessage(f, v...), nil)
}

// ErrorWithErr 输出携带 error 字段的错误日志。
// err 以独立字段记录方便日志采集检索；err 为 nil 时退化为普通 Error 输出。
func ErrorWithErr(msg string, err error) {
	if err == nil {
		stdLogger().log(ErrorLevel, msg, nil)
		return
	}
	stdLogger().log(ErrorLevel, msg, []Field{Err(err)})
}

// Panic 输出日志后执行 panic()，适合不可恢复且希望 recover 捕获的场景。
func Panic(f interface{}, v ...interface{}) {
	stdLogger().log(PanicLevel, formatMessage(f, v...), nil)
}

// DPanic 开发模式（Development=true）输出后 panic，生产模式等同 Error。
func DPanic(f interface{}, v ...interface{}) {
	stdLogger().log(DPanicLevel, formatMessage(f, v...), nil)
}

// Fatal 输出日志后执行 os.Exit(1)，适合启动阶段的致命错误。
// 注意：进程直接退出，defer 不会执行；退出前会先尝试 Sync 刷写缓冲。
func Fatal(f interface{}, v ...interface{}) {
	stdLogger().log(FatalLevel, formatMessage(f, v...), nil)
}

// Debugf 按 fmt 格式串输出调试日志（显式格式版，f 必为格式串）。
func Debugf(format string, args ...interface{}) {
	stdLogger().log(DebugLevel, formatMessage(format, args...), nil)
}

// Infof 按 fmt 格式串输出信息日志。
func Infof(format string, args ...interface{}) {
	stdLogger().log(InfoLevel, formatMessage(format, args...), nil)
}

// Warnf 按 fmt 格式串输出警告日志。
func Warnf(format string, args ...interface{}) {
	stdLogger().log(WarnLevel, formatMessage(format, args...), nil)
}

// Errorf 按 fmt 格式串输出错误日志。
func Errorf(format string, args ...interface{}) {
	stdLogger().log(ErrorLevel, formatMessage(format, args...), nil)
}

// Debugw 输出携带键值字段的调试日志，kv 成对传入。
func Debugw(msg string, kv ...interface{}) {
	stdLogger().log(DebugLevel, msg, kvToFields(kv))
}

// Infow 输出携带键值字段的信息日志。
func Infow(msg string, kv ...interface{}) {
	stdLogger().log(InfoLevel, msg, kvToFields(kv))
}

// Warnw 输出携带键值字段的警告日志。
func Warnw(msg string, kv ...interface{}) {
	stdLogger().log(WarnLevel, msg, kvToFields(kv))
}

// Errorw 输出携带键值字段的错误日志。
func Errorw(msg string, kv ...interface{}) {
	stdLogger().log(ErrorLevel, msg, kvToFields(kv))
}

// Fatalw 输出携带键值字段的致命日志，kv 成对传入。
func Fatalw(msg string, kv ...interface{}) {
	stdLogger().log(FatalLevel, msg, kvToFields(kv))
}

// Panicw 输出携带键值字段并触发 panic 的日志。
func Panicw(msg string, kv ...interface{}) {
	stdLogger().log(PanicLevel, msg, kvToFields(kv))
}

// DPanicw 输出携带键值字段的 DPanic 日志。
func DPanicw(msg string, kv ...interface{}) {
	stdLogger().log(DPanicLevel, msg, kvToFields(kv))
}

// SetLevel 运行期动态调整日志级别，立即对所有输出生效。
// 取值：debug、info、warn、error、dpanic、panic、fatal（大小写均可）。
func SetLevel(level string) error {
	lvl, err := ParseLevel(level)
	if err != nil {
		return err
	}
	// 全局级别开关沉淀在顶层核心的 level 中（ioCore 共享同一 AtomicLevel）
	if al, ok := findAtomicLevel(stdLogger().core); ok {
		al.Set(lvl)
	}
	return nil
}

// GetLevel 返回当前全局日志级别。
func GetLevel() Level {
	if al, ok := findAtomicLevel(stdLogger().core); ok {
		return al.Level()
	}
	return InfoLevel
}

// findAtomicLevel 遍历核心树找到共享的 AtomicLevel
// （穿过 tee/sampler/hook 等包装核心）。
func findAtomicLevel(c Core) (*AtomicLevel, bool) {
	switch t := c.(type) {
	case *ioCore:
		if al, ok := t.level.(*AtomicLevel); ok {
			return al, true
		}
	case teeCore:
		for _, sub := range t.cores {
			if al, ok := findAtomicLevel(sub); ok {
				return al, true
			}
		}
	case *samplerCore:
		return findAtomicLevel(t.Core)
	case *hooksCore:
		return findAtomicLevel(t.Core)
	}
	return nil, false
}

// Named 返回按模块区分子日志器的全局包装，日志中会带 logger 字段。
func Named(name string) *Logger { return stdLogger().Named(name) }

// WithFields 返回携带固定业务字段的日志器（map 形式的兼容 API）。
// 常用于注入请求 ID、用户 ID 等上下文信息。
func WithFields(fields map[string]interface{}) *Logger {
	fs := make([]Field, 0, len(fields))
	for k, v := range fields {
		fs = append(fs, Any(k, v))
	}
	return stdLogger().With(fs...)
}

// GetLogger 返回全局默认日志器实例，供需要实例语义（如传递给第三方库）
// 的场景使用。与包级函数操作同一输出目标。
func GetLogger() *Logger { return stdLogger() }

// Sync 刷写所有缓冲日志，进程退出前建议调用一次。
func Sync() error { return stdLogger().Sync() }

// Close 刷写并关闭日志文件句柄（仅控制台输出时为空操作），
// 进程优雅退出或重建配置前调用。
func Close() error { return stdLogger().Close() }

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

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

// kvToFields 交替键值对转字段表；key 必须是 string，value 任意。
// 非 string key / 奇数长度按 zap 的 !BADKEY 语义兜底，不 panic。
func kvToFields(kv []interface{}) []Field {
	if len(kv) == 0 {
		return nil
	}
	fields := make([]Field, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		if i == len(kv)-1 { // 末尾悬空值：!BADKEY
			fields = append(fields, Any("!BADKEY", kv[i]))
			break
		}
		key, ok := kv[i].(string)
		if !ok {
			key = "!BADKEY"
		}
		if err, isErr := kv[i+1].(error); isErr && err != nil {
			fields = append(fields, NamedErr(key, err))
			continue
		}
		fields = append(fields, Any(key, kv[i+1]))
	}
	return fields
}
