module github.com/hicode0101/go-logger/benchmark

go 1.25.1

require (
	github.com/hicode0101/go-logger v0.0.0
	go.uber.org/zap v1.28.0
)

require go.uber.org/multierr v1.11.0 // indirect

// 本库为基准对比专门引入 zap；go-logger 主模块本身保持零依赖。
replace github.com/hicode0101/go-logger => ../
