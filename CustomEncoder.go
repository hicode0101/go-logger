package logger

import (
	"go.uber.org/zap/zapcore"
	"time"
)

func CustomTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02 15:04:05.000"))
}

func CustomColorLevelEncoder(_level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {

	s, ok := colorMap[_level]
	if !ok {
		s = BrightMagenta.Format(_level.CapitalString())

	}
	enc.AppendString(s)

}
