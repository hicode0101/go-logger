package logger

// Config 日志初始化配置。
// 字段留空（零值）时使用对应默认值，见 DefaultConfig。
type Config struct {
	// ---- 基础配置（兼容旧版）----
	Level       string // 日志级别：debug/info/warn/error/dpanic/panic/fatal（大小写均可），默认 debug
	Console     bool   // 是否输出到控制台，默认 true
	File        bool   // 是否写入滚动日志文件，默认 true
	FilePath    string // 日志文件路径，默认 logs/app.log
	MaxSizeMB   int    // 单个日志文件最大体积（MB），超过后自动切割，默认 1024
	MaxBackups  int    // 最多保留的历史日志文件个数，默认 10
	MaxAgeDays  int    // 历史日志最长保留天数，默认 7
	Compress    bool   // 是否用 gzip 压缩历史日志文件，默认 false
	Development bool   // 开发模式：DPanic 级别直接 panic，默认 false

	// ---- 新版扩展配置 ----
	Encoding          string          // 文件输出编码：console / json / RegisterEncoder 注册名，默认 console
	ConsoleColor      bool            // 控制台级别着色，默认 true（DefaultConfig 已置位）
	DisableCaller     bool            // 关闭调用位置（file:line）记录
	DisableStacktrace bool            // 关闭 error 及以上级别的堆栈记录
	Sampling          *SamplingConfig // 重复日志采样限流，nil 表示不采样
	FileBufferSizeKB  int             // 文件缓冲写大小（KB），>0 启用；0=直写（崩溃不丢）
	FlushIntervalSec  int             // 缓冲写后台刷盘周期（秒），0=30s；仅 FileBufferSizeKB>0 时生效
	Sinks             []SinkConfig    // 多路输出配置；非空时 Console/File 等旧字段被忽略，完全按此列表构建
}

// DefaultConfig 返回一份默认配置，可直接修改个别字段后传给 Init。
func DefaultConfig() Config {
	return Config{
		Level:        "debug",
		Console:      true,
		File:         true,
		FilePath:     "logs/app.log",
		MaxSizeMB:    1024,
		MaxBackups:   10,
		MaxAgeDays:   7,
		ConsoleColor: true,
	}
}
