package logger

import (
	"fmt"
	"math"
	"time"
)

// isNaN/isInf 小封装，避免多处直接写 math 调用影响可读性。
func isNaN(v float64) bool           { return math.IsNaN(v) }
func isInf(v float64, sign int) bool { return math.IsInf(v, sign) }

// JSON 编码底层助手：全部直接向 Buffer 追加字节，
// 不经过 encoding/json（其 reflect + 中间缓冲结构有数倍开销）。
// 转义规则与 encoding/json 对齐：双引号、反斜杠、控制字符、
// 无效 UTF-8 序列替换为 \ufffd。

// hex 字符表，控制字符编码用。
const hexDigits = "0123456789abcdef"

// appendJSONString 追加带引号、已转义的 JSON 字符串。
// 参考 zap jsonEncoder.safeAddString：手工遍历、一次转义到位。
func appendJSONString(buf *Buffer, s string) {
	buf.AppendByte('"')
	// 快路径：扫描常见安全字符连续段，整段追加。
	for i := 0; i < len(s); {
		if c := s[i]; c >= 0x20 && c != '"' && c != '\\' && c < utf8RuneSelf {
			j := i
			for j < len(s) {
				c = s[j]
				if c < 0x20 || c == '"' || c == '\\' || c >= utf8RuneSelf {
					break
				}
				j++
			}
			buf.AppendString(s[i:j])
			i = j
			continue
		}
		switch c := s[i]; {
		case c == '"':
			buf.AppendString(`\"`)
			i++
		case c == '\\':
			buf.AppendString(`\\`)
			i++
		case c == '\n':
			buf.AppendString(`\n`)
			i++
		case c == '\r':
			buf.AppendString(`\r`)
			i++
		case c == '\t':
			buf.AppendString(`\t`)
			i++
		case c >= 0x20: // 非 ASCII 且非转义：按 UTF-8 原样透传（合法 UTF-8 或逐字节校验）
			r, size := decodeRune(s[i:])
			if r == badRune && size == 1 {
				// 无效 UTF-8 → Unicode 替换符
				buf.AppendString(`\ufffd`)
				i++
				continue
			}
			buf.AppendString(s[i : i+size])
			i += size
		default: // 控制字符 → \u00XX
			buf.AppendString(`\u00`)
			buf.AppendByte(hexDigits[c>>4])
			buf.AppendByte(hexDigits[c&0xF])
			i++
		}
	}
	buf.AppendByte('"')
}

// utf8RuneSelf 单字节即完整 rune 的边界（同 unicode/utf8.RuneSelf）。
const utf8RuneSelf = 0x80

// badRune 表示无效 rune 的哨兵值（同 utf8.RuneError 但 size=1 的组合语义）。
const badRune = 0xFFFD

// decodeRune 极简 UTF-8 解码：仅在多字节分支调用，正确处理合法序列并
// 识别非法首字节。不追求 utf8.DecodeRune 的完整语义（超长编码、代理区
// 等按原样透传即可，日志场景无歧义风险）。
func decodeRune(s string) (r rune, size int) {
	b := s[0]
	if b < utf8RuneSelf {
		return rune(b), 1
	}
	n := utf8ByteLen(b)
	if n == 1 || len(s) < n {
		return badRune, 1
	}
	// 校验续字节
	for i := 1; i < n; i++ {
		if s[i]&0xC0 != 0x80 {
			return badRune, 1
		}
	}
	switch n {
	case 2:
		return rune(b&0x1F)<<6 | rune(s[1]&0x3F), 2
	case 3:
		return rune(b&0x0F)<<12 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F), 3
	default:
		return rune(b&0x07)<<18 | rune(s[1]&0x3F)<<12 | rune(s[2]&0x3F)<<6 | rune(s[3]&0x3F), 4
	}
}

// utf8ByteLen 由首字节返回 UTF-8 序列长度；非法首字节返回 1。
func utf8ByteLen(b byte) int {
	switch {
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 1
	}
}

// appendFloatValue 追加 JSON 合法的浮点字面量：NaN/±Inf 在 JSON 中
// 无对应字面量，按 zap 语义编码为字符串 "NaN"/"+Inf"/"-Inf"。
func appendFloatValue(buf *Buffer, v float64) {
	switch {
	case isNaN(v):
		buf.AppendString(`"NaN"`)
	case isInf(v, 1):
		buf.AppendString(`"+Inf"`)
	case isInf(v, -1):
		buf.AppendString(`"-Inf"`)
	default:
		buf.AppendFloat(v, 64)
	}
}

// appendFieldJSON 追加单个 "key":value（不写分隔符）。
func appendFieldJSON(buf *Buffer, f Field) {
	appendJSONString(buf, f.Key)
	buf.AppendByte(':')
	switch f.Type {
	case StringType:
		appendJSONString(buf, f.Str)
	case Int64Type:
		buf.AppendInt(f.Int)
	case Uint64Type:
		buf.AppendUint(uint64(f.Int))
	case BoolType:
		buf.AppendBool(f.Int != 0)
	case Float64Type:
		appendFloatValue(buf, math.Float64frombits(uint64(f.Int)))
	case DurationType:
		appendFloatValue(buf, float64(f.Int)/float64(1e9))
	case TimeType:
		buf.AppendByte('"')
		buf.AppendTime(time.Unix(0, f.Int), timeLayoutMs)
		buf.AppendByte('"')
	case ErrorType:
		if err, ok := f.Interface.(error); ok && err != nil {
			appendJSONString(buf, err.Error())
		} else {
			buf.AppendString(`"<nil>"`)
		}
	default: // AnyType 及未知类型
		appendAnyValueJSON(buf, f.Interface)
	}
}

// appendAnyValueJSON 任意类型的 JSON 兜底编码：先匹配常见具体类型走
// 零反射路径，未命中再退化为 fmt（装箱 + 反射，仅在 Any 字段发生）。
func appendAnyValueJSON(buf *Buffer, v interface{}) {
	switch x := v.(type) {
	case nil:
		buf.AppendString("null")
	case string:
		appendJSONString(buf, x)
	case int:
		buf.AppendInt(int64(x))
	case int64:
		buf.AppendInt(x)
	case int32:
		buf.AppendInt(int64(x))
	case uint64:
		buf.AppendUint(x)
	case uint32:
		buf.AppendUint(uint64(x))
	case float64:
		appendFloatValue(buf, x)
	case float32:
		appendFloatValue(buf, float64(x))
	case bool:
		buf.AppendBool(x)
	case []byte:
		appendJSONString(buf, string(x))
	case error:
		appendJSONString(buf, x.Error())
	case fmt.Stringer:
		appendJSONString(buf, x.String())
	default:
		appendJSONString(buf, fmt.Sprintf("%v", x))
	}
}
