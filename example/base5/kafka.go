// kafka.go —— base5 的全部 Kafka 接入代码（字节流 Sink 扩展模式）。
//
// main.go 不出现任何 Kafka 客户端细节：本文件经 init() 调用
// logger.RegisterSink("kafka", …) 注册工厂后，main.go 只需在 Sinks 配置里
// 声明一路
//
//	{Type: "kafka", Format: "json", Options: {"brokers": …, "topic": …}}
//
// 即完成接入。分工：JSON 编码由 go-logger 内建编码器完成（Format: "json"），
// 本文件只负责"字节行 → 攒批 → produce"。
//
// 生产要点（docs/EXTEND.md 契约）：
//   - 拷贝再入队：Write 收到的 []byte 来自池化缓冲，调用方随即复用，不可持有；
//   - 有界队列非阻塞：队列满丢弃并计数，绝不阻塞业务调用方；
//   - 批量异步发送：后台协程攒批（200 条 / 200ms）经 kafka-go Writer 发送；
//   - Sync/Close 保证 flush；发送失败打到 stderr（不能经本 logger 回写，会递归）。
//
// 客户端依赖 kafka-go 属本示例模块（examples），不进入 go-logger 主模块。
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hicode0101/go-logger"
	"github.com/segmentio/kafka-go"
)

// kafkaSink 字节流 Sink：非阻塞入队 + 后台攒批异步发送。
type kafkaSink struct {
	writer    *kafka.Writer
	queue     chan []byte   // 有界队列
	quit      chan struct{} // Close 信号；不关闭 queue，避免与并发 Write 竞争
	dropped   atomic.Uint64
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// 注册 Sink 工厂（扩展方约定放在包 init 中）。
func init() {
	logger.RegisterSink("kafka", newKafkaSink)
}

// newKafkaSink Sink 工厂：brokers/topic 经 SinkConfig.Options 传入，由使用方
// 在配置中给出，扩展方与使用方解耦（主模块零依赖、扩展零侵入）。
func newKafkaSink(cfg logger.SinkConfig) (logger.Sink, error) {
	var brokers []string
	for _, b := range strings.Split(cfg.Options["brokers"], ",") {
		if b = strings.TrimSpace(b); b != "" {
			brokers = append(brokers, b)
		}
	}
	if len(brokers) == 0 {
		brokers = []string{"localhost:9092"}
	}
	topic := cfg.Options["topic"]
	if topic == "" {
		topic = "app-logs"
	}
	k := &kafkaSink{
		writer: &kafka.Writer{
			Addr:                   kafka.TCP(brokers...),
			Topic:                  topic,
			Balancer:               &kafka.LeastBytes{},
			RequiredAcks:           kafka.RequireOne,
			BatchSize:              200,
			BatchTimeout:           200 * time.Millisecond,
			AllowAutoTopicCreation: true, // 演示便利；生产建议提前建好 topic 并关闭此项
		},
		queue: make(chan []byte, 4096),
		quit:  make(chan struct{}),
	}
	k.wg.Add(1)
	go k.sendLoop()
	return k, nil
}

// Write 实现 Sink：拷贝、非阻塞入队。
func (k *kafkaSink) Write(p []byte) (int, error) {
	select {
	case <-k.quit: // 已 Close：丢弃（Close 与并发 Write 竞争时尽力而为）
		return len(p), nil
	default:
	}
	b := make([]byte, len(p)) // 关键：拷贝——p 来自池化缓冲，调用方随即复用
	copy(b, p)
	select {
	case k.queue <- b: // 非阻塞入队（一行 JSON 即一条消息）
	default:
		k.dropped.Add(1) // 队列满：丢弃 + 计数，保护业务热路径
	}
	return len(p), nil
}

// sendLoop 后台攒批发送：200 条或 200ms 触发一次。
func (k *kafkaSink) sendLoop() {
	defer k.wg.Done()
	const maxBatch = 200
	batch := make([]kafka.Message, 0, maxBatch)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case b := <-k.queue:
			batch = append(batch, kafka.Message{Value: b})
			if len(batch) >= maxBatch {
				batch = k.send(batch)
			}
		case <-ticker.C:
			batch = k.send(batch)
		case <-k.quit: // 排空剩余队列后退出
			for {
				select {
				case b := <-k.queue:
					batch = append(batch, kafka.Message{Value: b})
				default:
					k.send(batch)
					return
				}
			}
		}
	}
}

// send 发送一批；失败仅上报 stderr。
func (k *kafkaSink) send(batch []kafka.Message) []kafka.Message {
	if len(batch) == 0 {
		return batch[:0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := k.writer.WriteMessages(ctx, batch...); err != nil {
		fmt.Fprintf(os.Stderr, "kafka-sink: 发送 %d 条日志失败: %v\n", len(batch), err)
	}
	return batch[:0]
}

// Sync 实现 Sink：同步排空当前积压。
func (k *kafkaSink) Sync() error {
	pending := make([]kafka.Message, 0, 64)
	for draining := true; draining; {
		select {
		case b := <-k.queue:
			pending = append(pending, kafka.Message{Value: b})
		default:
			draining = false
		}
	}
	k.send(pending)
	return nil
}

// Close 实现 Sink：排空 → 关闭 writer → 上报丢弃统计（幂等）。
func (k *kafkaSink) Close() error {
	k.closeOnce.Do(func() {
		close(k.quit)
		k.wg.Wait()
		_ = k.writer.Close() // 刷出 kafka-go 内部批
		if d := k.dropped.Load(); d > 0 {
			fmt.Fprintf(os.Stderr, "kafka-sink: 运行期间因队列满丢弃 %d 条日志\n", d)
		}
	})
	return nil
}

// ---------------------------------------------------------------------------
// 验证辅助（供 main.go 调用；与发送链路无共享状态）
// ---------------------------------------------------------------------------

// kafkaReachable 预检 broker 可达性。
func kafkaReachable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// readLastOffset 取 partition 0 当前末尾 offset；topic 尚未创建或连接失败返回 -1。
func readLastOffset(broker, topic string) int64 {
	conn, err := kafka.DialLeader(context.Background(), "tcp", broker, topic, 0)
	if err != nil {
		return -1
	}
	defer func() { _ = conn.Close() }()
	last, err := conn.ReadLastOffset()
	if err != nil {
		return -1
	}
	return last
}

// verifyReadBack 从本轮基线 offset 读回全部消息，返回条数与第一条内容，
// 精确验证"本次运行"写入的日志（不受 topic 中历史消息干扰）。
func verifyReadBack(broker, topic string, fromOffset int64) (int, string) {
	if fromOffset < 0 {
		return 0, "" // 基线获取失败：跳过读回（消息可能已正常写入）
	}
	conn, err := kafka.DialLeader(context.Background(), "tcp", broker, topic, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读回验证连接失败: %v\n", err)
		return 0, ""
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Seek(fromOffset, io.SeekStart); err != nil {
		fmt.Fprintf(os.Stderr, "读回验证定位失败: %v\n", err)
		return 0, ""
	}
	// Conn.ReadMessage 以 minBytes 为参数，超时通过读截止时间控制
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		fmt.Fprintf(os.Stderr, "读回验证设置超时失败: %v\n", err)
		return 0, ""
	}
	n := 0
	first := ""
	for {
		msg, err := conn.ReadMessage(1)
		if err != nil {
			break // 读截止：已到本轮末尾
		}
		if n == 0 {
			first = strings.TrimSpace(string(msg.Value))
		}
		n++
	}
	return n, first
}
