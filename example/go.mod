module github.com/hicode0101/go-logger/examples

go 1.25.1

require (
	github.com/hicode0101/go-logger v0.0.0
	github.com/segmentio/kafka-go v0.4.51
)

require (
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
)

// 示例独立成模块：演示用的客户端 SDK 不进入主模块依赖树，主模块保持零依赖。
replace github.com/hicode0101/go-logger => ../
