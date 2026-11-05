package logger

// Encoder 编码器：把 Entry + 字段编码为一段可写出的字节。
//
// 设计约定（性能核心）：
//   - 编码目标由调用方传入的池化 Buffer 提供，编码器自身无状态、可并发复用；
//   - Encode 必须在末尾补上行结束符（本库统一为 "\n"），调用方直接 Write 即可；
//   - 每个级别一条日志调用一次 Encode，禁止在 Encode 内部分配大对象。
//
// 自定义编码器见 docs/EXTEND.md：实现本接口并调用 RegisterEncoder 即可。
type Encoder interface {
	// Encode 把 entry 与 fields 编码进 buf（覆盖写，不要求调用方预先 Reset）。
	Encode(buf *Buffer, entry *Entry, fields []Field)
	// Clone 返回可独立修改配置（如关闭着色）的副本。
	Clone() Encoder
}

// encoderRegistry 编码器注册表：内置 console/json，可注册自定义实现。
var encoderRegistry = map[string]func(EncoderConfig) Encoder{}

// RegisterEncoder 注册名为 name 的编码器工厂，重复注册覆盖。
// 供扩展方接入自定义输出格式（如 CSV、logfmt、Protobuf），
// 之后在 Config.Encoding / SinkConfig.Format 中引用该名称。
func RegisterEncoder(name string, factory func(EncoderConfig) Encoder) {
	encoderRegistry[name] = factory
}

func init() {
	RegisterEncoder("console", func(c EncoderConfig) Encoder { return NewConsoleEncoder(c) })
	RegisterEncoder("json", func(c EncoderConfig) Encoder { return NewJSONEncoder(c) })
}

// newEncoderByName 按名称构造编码器，未知名称回退到 console。
func newEncoderByName(name string, cfg EncoderConfig) Encoder {
	if f, ok := encoderRegistry[name]; ok {
		return f(cfg)
	}
	return NewConsoleEncoder(cfg)
}

// EncoderConfig 编码器配置。
type EncoderConfig struct {
	Color bool // 控制台级别着色（ANSI 转义序列），文件输出请保持 false
	// FullCallerPath caller 输出完整文件路径。默认 false：裁剪为
	// "最后两级目录/文件.go:行"（与 v1 及 zap ShortCallerEncoder 一致），
	// 避免暴露构建机器的目录结构。Entry.CallerFile 始终保存完整路径，
	// 本开关只影响内置编码器的展示，自定义 Core 拿到的数据不变。
	FullCallerPath bool
}

// consoleEncoder 控制台/人类可读编码器，输出布局（制表符分隔，同旧版）：
//
//	2024-06-01 12:30:45.123	INFO	[name]	main/main.go:12	消息	{"key": "value"}	堆栈
type consoleEncoder struct {
	color      bool
	fullCaller bool
}

// NewConsoleEncoder 创建控制台编码器。
func NewConsoleEncoder(cfg EncoderConfig) Encoder {
	return consoleEncoder{color: cfg.Color, fullCaller: cfg.FullCallerPath}
}

// appendCallerFile 按配置追加 caller 文件路径：默认裁剪为最后两级
// （v1/zap ShortCallerEncoder 行为），FullCallerPath 时输出完整路径。
func appendCallerFile(buf *Buffer, entry *Entry, full bool) {
	if full {
		buf.AppendString(entry.CallerFile)
		return
	}
	buf.AppendString(shortCallerPath(entry.CallerFile))
}

// separator 控制台字段分隔符。
const separator = '\t'

// Encode 实现 Encoder。
func (e consoleEncoder) Encode(buf *Buffer, entry *Entry, fields []Field) {
	buf.Reset()
	first := true

	// 追加制表符分隔的元素，避免重复的"非首个则加 \t"判断
	appendSep := func() {
		if !first {
			buf.AppendByte(separator)
		}
		first = false
	}

	// 时间
	if !entry.Time.IsZero() {
		appendSep()
		buf.AppendTime(entry.Time, timeLayoutMs)
	}
	// 级别（可着色），恒输出
	appendSep()
	if e.color {
		levelColor(entry.Level).AppendTo(buf, entry.Level.CapitalString())
	} else {
		buf.AppendString(entry.Level.CapitalString())
	}
	// 子日志器名
	if entry.LoggerName != "" {
		appendSep()
		buf.AppendString(entry.LoggerName)
	}
	// 调用位置（file:line 直接追加，零拼接分配；路径按配置裁剪）
	if entry.CallerDefined {
		appendSep()
		appendCallerFile(buf, entry, e.fullCaller)
		buf.AppendByte(':')
		buf.AppendInt(int64(entry.CallerLine))
	}
	// 消息
	if entry.Message != "" {
		appendSep()
		buf.AppendString(entry.Message)
	}
	// 结构化字段（JSON 形式内联）
	if len(fields) > 0 {
		appendSep()
		appendFieldsJSON(buf, fields)
	}
	// 堆栈独立成行，前缀缩进便于区分
	if entry.Stack != "" {
		buf.AppendByte('\n')
		buf.AppendString(entry.Stack)
	}
	buf.AppendByte('\n')
}

// Clone 实现 Encoder。
func (e consoleEncoder) Clone() Encoder { return e }

// jsonEncoder JSON 行编码器，每条日志一行 JSON 对象，
// 适合接入 Elasticsearch / 日志采集管道（Filebeat、Fluentd 等）。
type jsonEncoder struct {
	color      bool // JSON 模式下不生效，仅为 Clone 语义完整
	fullCaller bool
}

// NewJSONEncoder 创建 JSON 行编码器。
func NewJSONEncoder(cfg EncoderConfig) Encoder {
	return jsonEncoder{color: cfg.Color, fullCaller: cfg.FullCallerPath}
}

// Encode 实现 Encoder。
func (e jsonEncoder) Encode(buf *Buffer, entry *Entry, fields []Field) {
	buf.Reset()
	buf.AppendByte('{')
	first := true

	addKey := func(key string) {
		if !first {
			buf.AppendByte(',')
		}
		first = false
		appendJSONString(buf, key)
		buf.AppendByte(':')
	}

	addKey("level")
	appendJSONString(buf, entry.Level.String())
	addKey("time")
	buf.AppendByte('"')
	buf.AppendTime(entry.Time, timeLayoutMs)
	buf.AppendByte('"')
	if entry.LoggerName != "" {
		addKey("logger")
		appendJSONString(buf, entry.LoggerName)
	}
	if entry.CallerDefined {
		addKey("caller")
		buf.AppendByte('"')
		appendCallerFile(buf, entry, e.fullCaller)
		buf.AppendByte(':')
		buf.AppendInt(int64(entry.CallerLine))
		buf.AppendByte('"')
	}
	addKey("msg")
	appendJSONString(buf, entry.Message)
	for i := range fields {
		buf.AppendByte(',')
		appendFieldJSON(buf, fields[i])
	}
	if entry.Stack != "" {
		addKey("stacktrace")
		appendJSONString(buf, entry.Stack)
	}
	buf.AppendByte('}')
	buf.AppendByte('\n')
}

// Clone 实现 Encoder。
func (e jsonEncoder) Clone() Encoder { return e }

// appendFieldsJSON 编码字段块 "{k:v,k2:v2}"（控制台编码器内联用）。
func appendFieldsJSON(buf *Buffer, fields []Field) {
	buf.AppendByte('{')
	for i := range fields {
		if i > 0 {
			buf.AppendByte(',')
		}
		appendFieldJSON(buf, fields[i])
	}
	buf.AppendByte('}')
}
