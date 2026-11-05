package logger

import (
	"math"
	"testing"
	"time"
)

// TestFieldConstructors 验证强类型字段构造函数的类型标记与值存取。
func TestFieldConstructors(t *testing.T) {
	cases := []struct {
		name  string
		f     Field
		typ   FieldType
		check func(f Field) bool
	}{
		{"String", String("k", "v"), StringType, func(f Field) bool { return f.Str == "v" }},
		{"Int", Int("k", 42), Int64Type, func(f Field) bool { return f.Int == 42 }},
		{"Int64", Int64("k", -7), Int64Type, func(f Field) bool { return f.Int == -7 }},
		{"Uint64", Uint64("k", ^uint64(0)), Uint64Type, func(f Field) bool { return uint64(f.Int) == ^uint64(0) }},
		{"Float64", Float64("k", 1.5), Float64Type, func(f Field) bool {
			return math.Float64frombits(uint64(f.Int)) == 1.5 // 位模式存储，还原后比对
		}},
		{"Bool true", Bool("k", true), BoolType, func(f Field) bool { return f.Int == 1 }},
		{"Bool false", Bool("k", false), BoolType, func(f Field) bool { return f.Int == 0 }},
		{"Duration", Duration("k", 2*time.Second), DurationType, func(f Field) bool { return f.Int == int64(2*time.Second) }},
		{"Time", Time("k", fixedTime), TimeType, func(f Field) bool {
			return time.Unix(0, f.Int).Equal(fixedTime) // UnixNano 存储，还原后比对
		}},
		{"Any", Any("k", 3.14), AnyType, func(f Field) bool { return f.Interface.(float64) == 3.14 }},
		{"Err", Err(errTest{}), ErrorType, func(f Field) bool { return f.Interface.(error).Error() == "boom" }},
		{"NamedErr", NamedErr("cause", errTest{}), ErrorType, func(f Field) bool { return f.Key == "cause" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.f.Type != c.typ {
				t.Errorf("Type = %v, want %v", c.f.Type, c.typ)
			}
			if !c.check(c.f) {
				t.Errorf("字段值校验失败: %+v", c.f)
			}
		})
	}
}

// fixedTime 固定测试时间。
var fixedTime = time.Date(2024, 6, 1, 12, 0, 0, 0, time.Local)

// TestKvToFields 覆盖键值对转字段表：成对、悬空值、非法 key、error 值。
func TestKvToFields(t *testing.T) {
	// 成对
	fs := kvToFields([]interface{}{"a", 1, "b", "x"})
	if len(fs) != 2 || fs[0].Key != "a" || fs[1].Key != "b" {
		t.Errorf("成对转换结果: %+v", fs)
	}
	// 悬空值 → !BADKEY
	fs = kvToFields([]interface{}{"a", 1, 99})
	if len(fs) != 2 || fs[1].Key != "!BADKEY" {
		t.Errorf("悬空值转换结果: %+v", fs)
	}
	// 非 string key → !BADKEY
	fs = kvToFields([]interface{}{123, "v"})
	if len(fs) != 1 || fs[0].Key != "!BADKEY" {
		t.Errorf("非法 key 转换结果: %+v", fs)
	}
	// error 值 → ErrorType
	fs = kvToFields([]interface{}{"cause", errTest{}})
	if fs[0].Type != ErrorType {
		t.Errorf("error 值应转为 ErrorType: %+v", fs[0])
	}
	// 空输入
	if fs := kvToFields(nil); fs != nil {
		t.Errorf("空输入应返回 nil")
	}
}
