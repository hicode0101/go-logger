package logger

import (
	"strings"
	"testing"
)

// newTestEntry 构造统一测试条目。
func newTestEntry() Entry {
	return Entry{
		Level:         InfoLevel,
		Time:          fixedTime,
		CallerDefined: true,
		CallerFile:    "main/main.go",
		CallerLine:    12,
		Message:       "hello",
	}
}

// TestConsoleEncoderEncode 验证控制台布局与旧版输出一致（制表符分隔）。
func TestConsoleEncoderEncode(t *testing.T) {
	enc := NewConsoleEncoder(EncoderConfig{})
	b := getBuffer()
	defer putBuffer(b)
	enc.Encode(b, &Entry{
		Level:         WarnLevel,
		Time:          fixedTime,
		CallerDefined: true,
		CallerFile:    "main/main.go",
		CallerLine:    15,
		Message:       "磁盘使用率 85.5%",
	}, []Field{String("k", "v")})

	got := string(b.Bytes())
	want := "2024-06-01 12:00:00.000\tWARN\tmain/main.go:15\t磁盘使用率 85.5%\t{\"k\":\"v\"}\n"
	if got != want {
		t.Errorf("console 编码 =\n%q\nwant\n%q", got, want)
	}
}

// TestConsoleEncoderWithColor 验证着色编码包含 ANSI 转义序列。
func TestConsoleEncoderWithColor(t *testing.T) {
	enc := NewConsoleEncoder(EncoderConfig{Color: true})
	b := getBuffer()
	defer putBuffer(b)
	enc.Encode(b, &Entry{Level: ErrorLevel, Time: fixedTime, Message: "boom"}, nil)
	got := string(b.Bytes())
	if !strings.Contains(got, "\x1b[91mERROR\x1b[0m") {
		t.Errorf("ERROR 级别应着高亮红，实际 %q", got)
	}
}

// TestConsoleEncoderOptionalParts 验证 name/caller/stack 的可选输出与堆栈独立成行。
func TestConsoleEncoderOptionalParts(t *testing.T) {
	enc := NewConsoleEncoder(EncoderConfig{})
	b := getBuffer()
	defer putBuffer(b)
	entry := newTestEntry()
	entry.LoggerName = "http"
	entry.Stack = "main.main\n\tmain.go:1"
	enc.Encode(b, &entry, nil)
	got := string(b.Bytes())
	for _, want := range []string{"\thttp\t", "\tmain/main.go:12\t", "\nmain.main\n\tmain.go:1\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("控制台输出缺少 %q：\n%s", want, got)
		}
	}
}

// TestJSONEncoderEncode 验证 JSON 行编码：字段顺序、转义、堆栈键名。
func TestJSONEncoderEncode(t *testing.T) {
	enc := NewJSONEncoder(EncoderConfig{})
	b := getBuffer()
	defer putBuffer(b)
	entry := newTestEntry()
	entry.LoggerName = "http"
	entry.Stack = "main.main\n\tmain.go:1"
	enc.Encode(b, &entry, []Field{Int("uid", 10001), String("msg2", `he"llo`)})

	want := `{"level":"info","time":"2024-06-01 12:00:00.000","logger":"http","caller":"main/main.go:12","msg":"hello","uid":10001,"msg2":"he\"llo","stacktrace":"main.main\n\tmain.go:1"}` + "\n"
	if got := string(b.Bytes()); got != want {
		t.Errorf("json 编码 =\n%s\nwant\n%s", got, want)
	}
}

// TestJSONEncoderErrorField 验证 error 字段编码与 nil error 输出 <nil>。
func TestJSONEncoderErrorField(t *testing.T) {
	enc := NewJSONEncoder(EncoderConfig{})
	b := getBuffer()
	defer putBuffer(b)
	enc.Encode(b, &Entry{Level: ErrorLevel, Time: fixedTime, Message: "m"},
		[]Field{Err(errTest{}), NamedErr("nilErr", nil)})
	got := string(b.Bytes())
	if !strings.Contains(got, `"error":"boom"`) {
		t.Errorf("缺少 error 字段: %s", got)
	}
	if !strings.Contains(got, `"nilErr":"<nil>"`) {
		t.Errorf("nil error 应编码为 <nil>: %s", got)
	}
}

// TestEncoderRegistry 验证 RegisterEncoder 注册与 newEncoderByName 回退。
func TestEncoderRegistry(t *testing.T) {
	RegisterEncoder("testcustom", func(c EncoderConfig) Encoder {
		return NewJSONEncoder(c)
	})
	if _, ok := newEncoderByName("testcustom", EncoderConfig{}).(jsonEncoder); !ok {
		t.Error("注册的自定义编码器应可按名构造")
	}
	if _, ok := newEncoderByName("unknown-enc", EncoderConfig{}).(consoleEncoder); !ok {
		t.Error("未知编码器名应回退 console")
	}
}

// TestEncoderClone Clone 应返回同语义编码器。
func TestEncoderClone(t *testing.T) {
	enc := NewConsoleEncoder(EncoderConfig{Color: true})
	b := getBuffer()
	defer putBuffer(b)
	enc.Clone().Encode(b, &Entry{Level: InfoLevel, Time: fixedTime, Message: "x"}, nil)
	if !strings.Contains(string(b.Bytes()), "\x1b[94mINFO") {
		t.Errorf("克隆编码器应保留着色配置: %q", b.Bytes())
	}
}

// TestCallerPathTrim 验证 caller 路径默认裁剪为最后两级（v1/zap
// ShortCallerEncoder 行为），FullCallerPath 开关可还原完整路径。
func TestCallerPathTrim(t *testing.T) {
	full := "D:/AiWork/work-6/go-logger/example/basic/main.go"
	entry := newTestEntry()
	entry.CallerFile = full

	// console：默认裁剪为 "basic/main.go:12"，不暴露构建目录
	enc := NewConsoleEncoder(EncoderConfig{})
	b := getBuffer()
	defer putBuffer(b)
	enc.Encode(b, &entry, nil)
	got := string(b.Bytes())
	if !strings.Contains(got, "\tbasic/main.go:12\t") {
		t.Errorf("console caller 应裁剪为最后两级：%q", got)
	}
	if strings.Contains(got, "AiWork") {
		t.Errorf("console caller 不应包含构建目录：%q", got)
	}

	// console：FullCallerPath 还原完整路径
	b.Reset()
	NewConsoleEncoder(EncoderConfig{FullCallerPath: true}).Encode(b, &entry, nil)
	if !strings.Contains(string(b.Bytes()), full+":12") {
		t.Errorf("FullCallerPath 应输出完整路径：%q", b.Bytes())
	}

	// json：默认裁剪
	b.Reset()
	NewJSONEncoder(EncoderConfig{}).Encode(b, &entry, nil)
	if !strings.Contains(string(b.Bytes()), `"caller":"basic/main.go:12"`) {
		t.Errorf("json caller 应裁剪为最后两级：%q", b.Bytes())
	}

	// json：FullCallerPath 还原完整路径
	b.Reset()
	NewJSONEncoder(EncoderConfig{FullCallerPath: true}).Encode(b, &entry, nil)
	if !strings.Contains(string(b.Bytes()), `"caller":"`+full+`:12"`) {
		t.Errorf("json FullCallerPath 应输出完整路径：%q", b.Bytes())
	}

	// 反斜杠路径容错（自定义来源路径可能是 Windows 原生分隔符）
	entry.CallerFile = `D:\work\proj\sub\main.go`
	b.Reset()
	NewConsoleEncoder(EncoderConfig{}).Encode(b, &entry, nil)
	if !strings.Contains(string(b.Bytes()), "\tsub"+`\main.go`+":12\t") {
		t.Errorf("反斜杠路径也应正确裁剪：%q", b.Bytes())
	}

	// 无分隔符 / 仅一级分隔：原样保留（与 zap 语义一致）
	for _, p := range []string{"main.go", "/main.go", "D:/main.go"} {
		e := newTestEntry()
		e.CallerFile = p
		b.Reset()
		NewConsoleEncoder(EncoderConfig{}).Encode(b, &e, nil)
		if !strings.Contains(string(b.Bytes()), "\t"+p+":12\t") {
			t.Errorf("路径 %q 不应被裁剪改变：%q", p, b.Bytes())
		}
	}
}

// TestShortCallerPath shortCallerPath 规则与 zap TrimmedPath 对齐。
func TestShortCallerPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"main.go", "main.go"},
		{"/main.go", "/main.go"},
		{"a/b/main.go", "b/main.go"},
		{"/a/b/main.go", "b/main.go"},
		{"D:/AiWork/work-6/go-logger/example/basic/main.go", "basic/main.go"},
		{`D:\work\proj\sub\main.go`, `sub\main.go`},
	}
	for _, c := range cases {
		if got := shortCallerPath(c.in); got != c.want {
			t.Errorf("shortCallerPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
