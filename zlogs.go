package logger

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/natefinch/lumberjack"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	zlogs    *zap.Logger
	colorMap = make(map[zapcore.Level]string, 4)
)

func init() {

	colorMap[zapcore.DebugLevel] = BrightGreen.Format(zapcore.DebugLevel.CapitalString())
	colorMap[zapcore.InfoLevel] = BrightBlue.Format(zapcore.InfoLevel.CapitalString())
	colorMap[zapcore.WarnLevel] = BrightYellow.Format(zapcore.WarnLevel.CapitalString())
	colorMap[zapcore.ErrorLevel] = BrightRed.Format(zapcore.ErrorLevel.CapitalString())

	consoleConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    CustomColorLevelEncoder,
		EncodeTime:     CustomTimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder, //
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}

	fileConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     CustomTimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder, //
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}

	// 设置日志级别
	//highLevel := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
	//	return lvl >= zapcore.ErrorLevel
	//})

	lowLevel := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
		return lvl >= zapcore.DebugLevel
	})

	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(consoleConfig),                // 编码器配置
		zapcore.NewMultiWriteSyncer(zapcore.AddSync(os.Stdout)), // 打印到控制台和文件
		lowLevel, // 日志级别
	)

	debugFileHook := lumberjack.Logger{
		//Filename:   path.Join("logs", time.Now().Format("2006-01-02")+".log"), // 日志文件路径
		Filename:   path.Join("logs", "app.log"), // 日志文件路径
		MaxSize:    1024,                         // 每个日志文件保存的最大尺寸 单位：M
		MaxBackups: 10,                           // 日志文件最多保存多少个备份
		MaxAge:     7,                            // 文件最多保存多少天
		Compress:   false,                        // 是否压缩
		LocalTime:  true,
	}

	debugFileCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(fileConfig),                        // 编码器配置
		zapcore.NewMultiWriteSyncer(zapcore.AddSync(&debugFileHook)), // 打印到控制台和文件
		lowLevel, // 日志级别
	)

	coreGroup := zapcore.NewTee(consoleCore, debugFileCore)

	// 记录 对应源码文件及行号
	caller := zap.AddCaller()

	// 开启开发模式，会记录 DPanic-level
	development := zap.Development()

	trace := zap.AddStacktrace(zap.ErrorLevel)

	// 设置初始化字段
	filed := zap.Fields()

	// 构造日志对象
	//zlogs = zap.New(core, caller, trace, development, filed, zap.AddCallerSkip(1))
	zlogs = zap.New(coreGroup, caller, trace, development, filed, zap.AddCallerSkip(1))

}

func Debug(f interface{}, v ...interface{}) {

	zlogs.Debug(getFormatMsg(f, v...))
}

func Info(f interface{}, v ...interface{}) {

	zlogs.Info(getFormatMsg(f, v...))
}

func Warn(f interface{}, v ...interface{}) {

	zlogs.Warn(getFormatMsg(f, v...))
}

func Error(msg string, e error) {

	zlogs.Error(msg, zap.Error(e))
}

func ErrorMsg(f interface{}, v ...interface{}) {

	zlogs.Error(getFormatMsg(f, v...))
}

func Panic(f interface{}, v ...interface{}) {
	zlogs.Panic(getFormatMsg(f, v...))
}

func GetZlogs() *zap.Logger {
	return zlogs
}

func Sync() error {
	// flushes buffer
	return zlogs.Sync()
}

func getFormatMsg(f interface{}, v ...interface{}) string {
	var msg string
	switch f.(type) {
	case string:
		msg = f.(string)
		if len(v) == 0 {
			return msg
		}
		if strings.Contains(msg, "%") {
			return fmt.Sprintf(msg, v...)
		}
	default:
		msg = fmt.Sprint(f)
		if len(v) == 0 {
			return msg
		}
	}
	msg += strings.Repeat(" %v", len(v))
	return fmt.Sprintf(msg, v...)
}
