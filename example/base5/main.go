// 示例五（base5）：base3 的 Kafka 形态 —— JSON 行日志接入 Kafka。
//
// 与 base3 的唯一差别是多一路输出：日志除写本地 JSON 文件外，同一 JSON 行
// 经 kafka-sink 攒批 produce 到 Kafka topic。接入代码全部在 kafka.go（字节流
// Sink 扩展模式，经 RegisterSink 注册），本文件不出现任何 Kafka 客户端细节——
// 只需在 Sinks 里声明一路 {Type: "kafka", Format: "json", Options: {brokers, topic}}。
//
// 运行方式（在 example 目录下）：
//
//	go run ./base5
//
// 前置条件：本地可用的 Kafka（默认 localhost:9092），例如：
//
//	docker run -d --name kafka -p 9092:9092 apache/kafka:3.9.0
//
// 消费验证：
//
//	kafka-console-consumer.sh --bootstrap-server localhost:9092 --topic base5-logs --from-beginning
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/hicode0101/go-logger"
)

const (
	kafkaBroker = "localhost:9092" // broker 地址（多 broker 逗号分隔经 Options 传入）
	kafkaTopic  = "base5-logs"     // 本示例专用 topic，与其它示例数据隔离
)

func main() {
	// 预检 broker 可达性，给出友好的启动指引
	if !kafkaReachable(kafkaBroker) {
		fmt.Fprintf(os.Stderr, "无法连接 Kafka broker %s。\n请先启动本地 Kafka，例如：\n"+
			"  docker run -d --name kafka -p 9092:9092 apache/kafka:3.9.0\n", kafkaBroker)
		os.Exit(1)
	}

	// ---------------------------------------------------------------------------
	// 正式演示配置：与 base3 相同的 JSON 行，外加一路 "kafka"（文件兜底 + Kafka 双写）
	// BufferSizeKB=0：kafka-sink 内部已攒批，不再叠加缓冲
	// 在 kafka.go 的 init 初始化方法中 logger.RegisterSink 自动注册，对业务代码几乎零入侵
	// ---------------------------------------------------------------------------
	fmt.Printf("========== JSON 日志接入 Kafka（%s，topic %s） ==========\n", kafkaBroker, kafkaTopic)
	_ = logger.Init(logger.Config{
		Level: "debug",
		Sinks: []logger.SinkConfig{
			{Type: "file", Format: "json", Path: "logs/base5-demo.log", // 本地兜底：Kafka 不可用期间日志仍落盘
				MaxSizeMB: 1, MaxBackups: 3, MaxAgeDays: 7, LocalTime: true},
			{Type: "kafka", Format: "json", BufferSizeKB: 0,
				Options: map[string]string{"brokers": kafkaBroker, "topic": kafkaTopic}},
		},
	})

	// 第一条消息触发 topic 自动创建，随后记录末尾 offset 作为本轮读回基线
	logger.Debug("Debug：开发期细节，生产环境会被级别过滤")
	baseOffset := readLastOffset(kafkaBroker, kafkaTopic)

	logger.Info("Info：业务关键节点打点 user=%s", "Tom")
	logger.Warn("Warn：可自愈的异常")
	logger.Error("Error：error 及以上级别自动附带调用堆栈")
	logger.ErrorWithErr("读取配置失败", errors.New("file not found"))

	httpLog := logger.Named("http")
	httpLog.Info("listening on :8080")
	_ = logger.Sync()
	_ = logger.Close() // 排空 kafka-sink 攒批并关闭 writer，之后即可读回验证

	// 从基线 offset 读回"本轮"写入的消息，精确验证接入成功
	if n, first := verifyReadBack(kafkaBroker, kafkaTopic, baseOffset); n > 0 {
		fmt.Printf("已从 Kafka 读回本轮日志 %d 条（基线 offset=%d），接入成功。\n", n, baseOffset)
		fmt.Println("其中一条消息内容（与本地 JSON 文件行完全一致）：")
		fmt.Println("  " + first)
	} else {
		fmt.Fprintln(os.Stderr, "读回验证跳过（无法获取基线或超时），可手动消费确认：\n"+
			"  kafka-console-consumer.sh --bootstrap-server "+kafkaBroker+" --topic "+kafkaTopic+" --from-beginning")
	}
}
