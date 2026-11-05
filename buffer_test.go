package logger

import (
	"math"
	"strings"
	"testing"
	"time"
)

// nanValue/infValue 测试辅助。
func nanValue() float64      { return math.NaN() }
func infValue(s int) float64 { return math.Inf(s) }

// TestBufferAppends 覆盖 Buffer 各追加方法的正确性。
func TestBufferAppends(t *testing.T) {
	b := getBuffer()
	defer putBuffer(b)

	b.AppendByte('x')
	b.AppendString("hello")
	b.AppendInt(-123)
	b.AppendUint(456)
	b.AppendBool(true)
	b.AppendFloat(3.25, 64)
	b.AppendQuote(`a"b`)
	got := string(b.Bytes())
	if got != `xhello-123456true3.25"a\"b"` {
		t.Errorf("Buffer 内容 = %q", got)
	}
	if b.Len() != len(got) {
		t.Errorf("Len() = %d, want %d", b.Len(), len(got))
	}
}

// TestBufferTime 内置时间格式快路径与 time.Format 基准输出一致性。
func TestBufferTime(t *testing.T) {
	// 手工构造多个时间点覆盖边界：闰年、月末、毫秒进位
	times := []time.Time{
		time.Date(2024, 2, 29, 23, 59, 59, 999e6, time.Local),
		time.Date(1999, 12, 31, 0, 0, 0, 0, time.Local),
		time.Date(2026, 1, 1, 9, 8, 7, 6e6, time.Local),
	}
	for _, tt := range times {
		b := getBuffer()
		b.AppendTime(tt, timeLayoutMs)
		fast := b.Bytes()
		putBuffer(b)

		want := tt.Format(timeLayoutMs)
		if string(fast) != want {
			t.Errorf("appendTimeLayout(%v) = %q, want %q", tt, fast, want)
		}
	}
}

// TestAppendTimeDigits 覆盖两位/四位数字编码的边界值。
func TestAppendTimeDigits(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "00"}, {5, "05"}, {10, "10"}, {59, "59"}, {99, "99"},
	}
	for _, c := range cases {
		b := appendTwoDigits(nil, c.n)
		if string(b) != c.want {
			t.Errorf("appendTwoDigits(%d) = %q, want %q", c.n, b, c.want)
		}
	}
	four := map[int]string{
		0: "0000", 999: "0999", 1000: "1000", 2024: "2024", 9999: "9999",
	}
	for n, want := range four {
		b := appendFourDigits(nil, n)
		if string(b) != want {
			t.Errorf("appendFourDigits(%d) = %q, want %q", n, b, want)
		}
	}
}

// TestBufferPool 复用缓冲不应残留旧内容。
func TestBufferPool(t *testing.T) {
	b := getBuffer()
	b.AppendString("previous content")
	putBuffer(b)
	b2 := getBuffer()
	if b2.Len() != 0 {
		t.Errorf("池化缓冲取出后长度应为 0，实际 %d", b2.Len())
	}
	putBuffer(b2)
}

// TestAppendJSONString 覆盖 JSON 转义的正确性：引号、反斜杠、
// 控制字符、换行制表符、中文与非法 UTF-8。
func TestAppendJSONString(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`plain`, `"plain"`},
		{`he"llo`, `"he\"llo"`},
		{`back\slash`, `"back\\slash"`},
		{"line\nbreak", `"line\nbreak"`},
		{"tab\there", `"tab\there"`},
		{"cr\r", `"cr\r"`},
		{"中文", `"中文"`},
		{"\x01ctrl", "\"\\u0001ctrl\""},
		{"\x1f", "\"\\u001f\""},
		{"bad\xffutf8", `"bad\ufffdutf8"`},
		{"emoji😀", `"emoji😀"`},
	}
	for _, c := range cases {
		b := getBuffer()
		appendJSONString(b, c.in)
		if got := b.String2(); got != c.want {
			t.Errorf("appendJSONString(%q) = %s, want %s", c.in, got, c.want)
		}
		putBuffer(b)
	}
}

// String2 Buffer 内容转 string（测试辅助；命名避开 string 关键字联想）。
func (b *Buffer) String2() string { return string(b.bs) }

// TestAppendFloatValue 覆盖 NaN/Inf 的 JSON 字符串化语义。
func TestAppendFloatValue(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{3.5, "3.5"},
		{1e21, "1000000000000000000000"},
	}
	for _, c := range cases {
		b := getBuffer()
		appendFloatValue(b, c.in)
		if got := b.String2(); got != c.want {
			t.Errorf("appendFloatValue(%v) = %s, want %s", c.in, got, c.want)
		}
		putBuffer(b)
	}
	// NaN/Inf 编码为字符串字面量
	for _, c := range []struct {
		in   float64
		want string
	}{
		{nanValue(), `"NaN"`},
		{infValue(1), `"+Inf"`},
		{infValue(-1), `"-Inf"`},
	} {
		b := getBuffer()
		appendFloatValue(b, c.in)
		if got := b.String2(); got != c.want {
			t.Errorf("appendFloatValue(%v) = %s, want %s", c.in, got, c.want)
		}
		putBuffer(b)
	}
}

// TestAppendFieldsJSON 字段块整体编码与多种字段类型混合。
func TestAppendFieldsJSON(t *testing.T) {
	b := getBuffer()
	defer putBuffer(b)
	appendFieldsJSON(b, []Field{
		String("s", "v"),
		Int("i", 7),
		Bool("b", true),
		Float64("f", 1.5),
		Duration("d", 1500*time.Millisecond),
	})
	want := `{"s":"v","i":7,"b":true,"f":1.5,"d":1.5}`
	if got := b.String2(); got != want {
		t.Errorf("appendFieldsJSON = %s\nwant %s", got, want)
	}
}

// TestAppendAnyValueJSON 任意类型兜底编码。
func TestAppendAnyValueJSON(t *testing.T) {
	cases := []struct {
		in   interface{}
		want string
	}{
		{nil, "null"},
		{"str", `"str"`},
		{42, "42"},
		{int64(1), "1"},
		{uint64(2), "2"},
		{false, "false"},
		{[]byte("bin"), `"bin"`},
		{struct{ A int }{1}, `"{1}"`}, // 未匹配类型走 fmt，字符串化后编码
	}
	for _, c := range cases {
		b := getBuffer()
		appendAnyValueJSON(b, c.in)
		if got := b.String2(); got != c.want {
			t.Errorf("appendAnyValueJSON(%v) = %s, want %s", c.in, got, c.want)
		}
		putBuffer(b)
	}
	// error 类型取 Error() 文本
	b := getBuffer()
	appendAnyValueJSON(b, errTest{})
	if got := b.String2(); got != `"boom"` {
		t.Errorf("appendAnyValueJSON(error) = %s", got)
	}
	putBuffer(b)
}

type errTest struct{}

func (errTest) Error() string { return "boom" }

// TestTimeEncoderFormatRemoved 兼容性回归说明：旧版导出的 TimeEncoder
// 已由内置时间快路径替代，此处验证其格式输出与旧版一致。
func TestTimeEncoderFormatRemoved(t *testing.T) {
	tm := time.Date(2024, 6, 1, 12, 30, 45, 123e6, time.Local)
	b := getBuffer()
	defer putBuffer(b)
	b.AppendTime(tm, timeLayoutMs)
	want := "2024-06-01 12:30:45.123"
	if got := b.String2(); got != want {
		t.Errorf("时间编码 = %q, want %q", got, want)
	}
	if !strings.HasSuffix(want, ".123") {
		t.Error("毫秒精度应保留")
	}
}
