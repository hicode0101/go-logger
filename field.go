package logger

import (
	"math"
	"time"
)

// FieldType 字段类型标记。结构化日志的核心设计：Field 是纯值类型
// （无 interface{} 装箱的强类型分支），编码器按类型直接走对应编码分支，
// 热路径完全避开反射。类型不匹配时 AnyType 兜底，仅此时才走 fmt/反射。
// 与 zap Field 的 Type 字段同构（zapcore.StringType/Int64Type/...）。
type FieldType uint8

const (
	// UnknownType 未知类型，编码时按 AnyType 处理。
	UnknownType FieldType = iota
	// StringType 字符串，值在 Str 中。
	StringType
	// Int64Type 有符号整数，值在 Int 中。
	Int64Type
	// Uint64Type 无符号整数，值在 Int 中（按位重解释为 uint64）。
	Uint64Type
	// Float64Type 浮点数，值在 Int 中（IEEE754 位模式，编码时还原）。
	Float64Type
	// BoolType 布尔，值在 Int 中（0/1）。
	BoolType
	// ErrorType error，取 .Error() 文本；nil 时输出 "<nil>"。
	ErrorType
	// DurationType 时长，值在 Int 中（纳秒），编码为秒。
	DurationType
	// TimeType 时间，值在 Int 中（UnixNano），编码时还原为 time.Time。
	TimeType
	// AnyType 任意类型兜底，编码走 fmt（有反射/装箱成本），应尽量少用。
	AnyType
)

// Field 结构化字段：键 + 强类型值。
// 设计要点（对齐 zap）：
//   - 大小固定的小结构体，按值传递，构造函数全部内联零分配；
//   - 所有标量（含浮点/时间/布尔）都以位模式存入 Int，避免 interface{}
//     装箱带来的堆分配——10 个字段一条日志仅 1 次切片分配；
//   - 构造函数不做任何校验或转换，把成本留在编码阶段且仅对启用级别的日志发生。
type Field struct {
	Key       string    // 字段名
	Type      FieldType // 值类型标记
	Int       int64     // 整数/布尔/时长/浮点位模式/UnixNano 的值
	Str       string    // 字符串（或 error 文本）的值
	Interface interface{}
}

// String 构造字符串字段。
func String(key, val string) Field { return Field{Key: key, Type: StringType, Str: val} }

// Int 构造整数字段。
func Int(key string, val int) Field { return Field{Key: key, Type: Int64Type, Int: int64(val)} }

// Int64 构造 int64 字段。
func Int64(key string, val int64) Field { return Field{Key: key, Type: Int64Type, Int: val} }

// Int32 构造 int32 字段。
func Int32(key string, val int32) Field { return Field{Key: key, Type: Int64Type, Int: int64(val)} }

// Uint64 构造 uint64 字段。
func Uint64(key string, val uint64) Field {
	return Field{Key: key, Type: Uint64Type, Int: int64(val)}
}

// Float64 构造浮点字段：IEEE754 位模式存入 Int，编码时还原，零装箱。
func Float64(key string, val float64) Field {
	return Field{Key: key, Type: Float64Type, Int: int64(math.Float64bits(val))}
}

// Bool 构造布尔字段。
func Bool(key string, val bool) Field {
	f := Field{Key: key, Type: BoolType}
	if val {
		f.Int = 1
	}
	return f
}

// Err 构造 error 字段：err 非 nil 时输出其 Error() 文本，nil 时省略该字段
// （由编码器处理），与旧版 ErrorWithErr 的"不打印空 error 字段"语义一致。
func Err(err error) Field { return Field{Key: "error", Type: ErrorType, Interface: err} }

// NamedErr 构造自定义键名的 error 字段。
func NamedErr(key string, err error) Field {
	return Field{Key: key, Type: ErrorType, Interface: err}
}

// Duration 构造时长字段，编码为秒（保留浮点精度）。
func Duration(key string, val time.Duration) Field {
	return Field{Key: key, Type: DurationType, Int: int64(val)}
}

// Time 构造时间字段：UnixNano 存入 Int，编码时还原，零装箱。
func Time(key string, val time.Time) Field {
	return Field{Key: key, Type: TimeType, Int: val.UnixNano()}
}

// Any 构造任意类型字段：走 fmt 兜底编码（装箱 + 反射成本），
// 高频路径请优先使用上面的强类型构造函数。
func Any(key string, val interface{}) Field {
	return Field{Key: key, Type: AnyType, Interface: val}
}
