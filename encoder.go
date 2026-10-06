package logger

import (
	"time"

	"go.uber.org/zap/zapcore"
)

// timeLayoutMs 日志时间格式，精确到毫秒。
const timeLayoutMs = "2006-01-02 15:04:05.000"

// TimeEncoder 自定义时间编码器，输出形如 2024-06-01 12:30:45.123，
// 比 zap 默认的 ISO8601 格式更紧凑易读，适合中文项目的日志习惯。
func TimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format(timeLayoutMs))
}

// levelColorMap 日志级别到控制台颜色的映射。
// 只需在此处登记即可扩展（如为 Fatal 指定颜色），未登记的级别用 BrightMagenta 兜底。
var levelColorMap = map[zapcore.Level]ConsoleColor{
	zapcore.DebugLevel: BrightGreen,
	zapcore.InfoLevel:  BrightBlue,
	zapcore.WarnLevel:  BrightYellow,
	zapcore.ErrorLevel: BrightRed,
}

// ColorLevelEncoder 带 ANSI 颜色的日志级别编码器，输出的级别名加粗高亮，
// 便于在控制台快速扫视定位。仅用于控制台输出；写文件请使用
// zapcore.CapitalLevelEncoder（纯文本大写），避免转义序列污染日志文件。
func ColorLevelEncoder(lvl zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	color, ok := levelColorMap[lvl]
	if !ok {
		// 未映射的级别（DPanic/Panic/Fatal 等）兜底为高亮洋红
		color = BrightMagenta
	}
	enc.AppendString(color.Format(lvl.CapitalString()))
}
