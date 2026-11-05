package logger

import "time"

// Entry 单条日志事件的元数据（不含字段），沿 zapcore.Entry 的语义裁剪而来：
//   - Caller 拆为 CallerFile/CallerLine 存储，编码阶段直接追加，避免
//     "file:line" 字符串拼接的每条日志分配（zap EntryCaller 同款设计）；
//   - Stack 存已格式化的堆栈文本，采集成本只在 Error+ 级别发生。
//     按值传递，构造在级别过滤之后，禁用级别不产生 Entry。
type Entry struct {
	Level         Level     // 级别
	Time          time.Time // 事件时间
	LoggerName    string    // Named 设置的子日志器名，空表示根日志器
	CallerDefined bool      // 是否采集到了调用位置
	CallerFile    string    // 调用位置文件路径（编译期完整路径；内置编码器输出时默认裁剪为最后两级，见 EncoderConfig.FullCallerPath）
	CallerLine    int       // 调用位置行号
	Message       string    // 日志消息
	Stack         string    // 堆栈文本，仅 Error 及以上级别（或显式配置）采集
}

// CallerString 返回 "file:line" 格式的调用位置（惰性拼接，仅调试/自定义
// Core 需要时使用；内置编码器直接追加 file 与 line，不走本方法）。
func (e Entry) CallerString() string {
	if !e.CallerDefined {
		return ""
	}
	return e.CallerFile + ":" + itoaCaller(e.CallerLine)
}

// itoaCaller 正整数转十进制字符串（CallerString 辅助，避免 fmt 分配）。
func itoaCaller(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
