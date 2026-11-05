package logger

import (
	"fmt"
	"sync/atomic"
)

// Level 日志级别，内部用 int32 存储，天然支持 atomic 原子读写，
// 这是运行期动态调级别零锁开销的基础（借鉴 zap 的 AtomicLevel）。
// 数值越小级别越低，级别过滤只需一次有符号整数比较。
type Level int32

const (
	// DebugLevel 调试信息，开发期排查问题用，生产环境通常关闭。
	DebugLevel Level = iota - 1 // -1
	// InfoLevel 一般信息，业务关键节点打点。
	InfoLevel // 0
	// WarnLevel 警告，可自行恢复的异常，如重试后成功。
	WarnLevel // 1
	// ErrorLevel 错误，需要关注但进程可继续运行；本库默认对该级别及以上自动附带堆栈。
	ErrorLevel // 2
	// DPanicLevel 开发模式（Development=true）下会 panic，生产模式等同 Error。
	DPanicLevel // 3
	// PanicLevel 输出日志后执行 panic()。
	PanicLevel // 4
	// FatalLevel 输出日志后执行 os.Exit(1)。
	FatalLevel // 5

	minLevel = DebugLevel
	maxLevel = FatalLevel
)

// String 返回小写级别名（debug/info/warn/error/dpanic/panic/fatal）。
func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "debug"
	case InfoLevel:
		return "info"
	case WarnLevel:
		return "warn"
	case ErrorLevel:
		return "error"
	case DPanicLevel:
		return "dpanic"
	case PanicLevel:
		return "panic"
	case FatalLevel:
		return "fatal"
	default:
		return fmt.Sprintf("Level(%d)", int32(l))
	}
}

// CapitalString 返回大写级别名（DEBUG/INFO/...），文件与控制台输出用。
// 用查表替代 fmt，避免热路径格式化开销。
func (l Level) CapitalString() string {
	switch l {
	case DebugLevel:
		return "DEBUG"
	case InfoLevel:
		return "INFO"
	case WarnLevel:
		return "WARN"
	case ErrorLevel:
		return "ERROR"
	case DPanicLevel:
		return "DPANIC"
	case PanicLevel:
		return "PANIC"
	case FatalLevel:
		return "FATAL"
	default:
		return fmt.Sprintf("LEVEL(%d)", int32(l))
	}
}

// Enabled 判断本级别是否应当输出：l 不低于参数级别即启用。
// 实现 LevelEnabler 接口，可直接作为核心的级别开关。
func (l Level) Enabled(lv Level) bool { return lv >= l }

// ParseLevel 将字符串解析为级别，大小写均可；非法值返回错误。
func ParseLevel(s string) (Level, error) {
	switch normalizeLevel(s) {
	case "debug":
		return DebugLevel, nil
	case "info":
		return InfoLevel, nil
	case "warn", "warning":
		return WarnLevel, nil
	case "error":
		return ErrorLevel, nil
	case "dpanic":
		return DPanicLevel, nil
	case "panic":
		return PanicLevel, nil
	case "fatal":
		return FatalLevel, nil
	default:
		return InfoLevel, fmt.Errorf("logger: 无效的日志级别 %q（可选 debug/info/warn/error/dpanic/panic/fatal）", s)
	}
}

// normalizeLevel 小写化，避免热路径 strings.ToLower 的间接调用与边界检查。
func normalizeLevel(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		c := b[i]
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// LevelEnabler 级别开关抽象：任何提供 Enabled(Level) bool 的类型都能
// 控制一个核心是否处理某条日志。AtomicLevel 与 Level 自身均实现了它。
type LevelEnabler interface {
	Enabled(Level) bool
}

// AtomicLevel 运行期可动态修改的级别开关，内部 atomic.Int32 实现，
// Set 与 Enabled 都是无锁操作，对控制台与文件等多个核心共享同一实例
// 即可让 SetLevel 同时影响所有输出（沿用旧版行为）。
type AtomicLevel struct {
	lvl atomic.Int32
}

// NewAtomicLevel 创建初始级别为 l 的动态级别开关。
func NewAtomicLevel(l Level) *AtomicLevel {
	a := &AtomicLevel{}
	a.lvl.Store(int32(l))
	return a
}

// Enabled 判断 lv 是否达到当前级别，热路径上仅一次原子读。
func (a *AtomicLevel) Enabled(lv Level) bool { return lv >= Level(a.lvl.Load()) }

// Level 返回当前级别。
func (a *AtomicLevel) Level() Level { return Level(a.lvl.Load()) }

// Set 原子修改级别，立即对所有共用此开关的核心生效。
func (a *AtomicLevel) Set(l Level) { a.lvl.Store(int32(l)) }
