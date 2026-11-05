// 示例二：日志接入 Kafka —— 字节流 Sink 扩展模式（docs/EXTEND.md 3.1 的可运行版）。
//
// 模式说明：Kafka 消费"字节流"，实现 logger.Sink 接口即可接入；JSON 编码由
// go-logger 内建完成（Format: "json"），Sink 只负责"字节 → 目的地"。
//
// 生产要点（本示例均已实现，详见 docs/EXTEND.md）：
//   - 拷贝再入队：Write 收到的 []byte 来自池化缓冲，调用方随即复用，不可持有；
//   - 有界队列非阻塞：队列满丢弃并计数，绝不阻塞业务调用方；
//   - 批量异步发送：后台协程攒批（200 条 / 200ms）经 kafka-go Writer 发送；
//   - Sync/Close 保证 flush；发送失败打到 stderr（不能经本 logger 回写，会递归）；
//   - 本地文件双写兜底：Kafka 不可用期间日志仍落盘，事后可补投。
//
// 前置条件：本地可用的 Kafka（默认 localhost:9092），例如：
//
//	docker run -d --name kafka -p 9092:9092 apache/kafka:3.9.0
//
// 运行方式（在 example 目录下）：
//
//	go run ./kafka
//	KAFKA_BROKERS=10.0.0.1:9092 KAFKA_TOPIC=app-logs go run ./kafka
//
// 消费验证：
//
//	kafka-console-consumer --bootstrap-server localhost:9092 --topic app-logs --from-beginning
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
	quit      chan struct{} // Close 信号；不关闭 queue，避免与并发 Write 竞争触发 panic
	dropped   atomic.Uint64
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// newKafkaSink Sink 工厂：brokers/topic 经 SinkConfig.Options 传入，
// 由使用方在配置中给出，扩展方与使用方解耦（主模块零依赖、扩展零侵入）。
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
	case k.queue <- b: // 非阻塞入队
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

// Close 实现 Sink：排空 → 关闭 writer → 上报丢弃统计。
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

// 注册 Sink 工厂（扩展方建议放在包 init 中）。
func init() {
	logger.RegisterSink("kafka", newKafkaSink)
}

func main() {
	brokers := envOr("KAFKA_BROKERS", "localhost:9092")
	topic := envOr("KAFKA_TOPIC", "app-logs")

	// 预检 broker 可达性，给出友好的启动指引
	if !reachable(firstOf(brokers)) {
		fmt.Fprintf(os.Stderr, "无法连接 Kafka broker %s。\n请先启动本地 Kafka，例如：\n"+
			"  docker run -d --name kafka -p 9092:9092 apache/kafka:3.9.0\n"+
			"或指定其它地址：KAFKA_BROKERS=10.0.0.1:9092 go run ./kafka\n", brokers)
		os.Exit(1)
	}

	// 多路输出：本地文件兜底双写 + Kafka（Options 键值由扩展方自定义）
	_ = logger.Init(logger.Config{
		Level: "info",
		Sinks: []logger.SinkConfig{
			{Type: "file", Format: "json", Path: "logs/kafka-demo.log", BufferSizeKB: 64},
			{Type: "kafka", Format: "json", BufferSizeKB: 0, // Sink 内部已攒批，不再叠加缓冲
				Options: map[string]string{"brokers": brokers, "topic": topic}},
		},
	})
	defer logger.Close()

	fmt.Printf("向 Kafka（%s / topic=%s）写日志……本地兜底文件：logs/kafka-demo.log\n", brokers, topic)
	// 第一条消息触发 topic 自动创建，随后记录末尾 offset 作为本轮读回基线
	logger.GetLogger().Info("订单创建成功", logger.String("orderNo", "NO20240101"), logger.Int("uid", 10001))
	baseOffset := readLastOffset(firstOf(brokers), topic)

	// 注意：强类型字段必须走实例方法（包级 Info(f, v...) 是 legacy 格式化 API，
	// 传入 Field 会被当作普通参数 fmt 进消息）——这正是易混点，示例特意注明。
	logger.Infow("支付回调处理完成", "orderNo", "NO20240101", "amount", 99.5)
	logger.Errorw("库存扣减失败", "sku", "SKU-9", "retry", 3) // error 级别自动附堆栈字段
	for i := 0; i < 20; i++ {
		logger.GetLogger().Info("批量业务日志", logger.Int("seq", i), logger.String("worker", "w1"))
	}
	_ = logger.Sync() // 同步刷出：本地缓冲落盘 + kafka 积压清空

	verifyReadBack(firstOf(brokers), topic, baseOffset)
	fmt.Printf("验证完成。也可手动消费查看：\n  kafka-console-consumer --bootstrap-server %s --topic %s --from-beginning\n",
		brokers, topic)
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

// verifyReadBack 从本轮基线 offset 读回一条消息，精确验证本次运行写入的日志。
func verifyReadBack(broker, topic string, fromOffset int64) {
	if fromOffset < 0 {
		fmt.Fprintln(os.Stderr, "读回验证跳过（无法获取 topic 末尾 offset）。")
		return
	}
	conn, err := kafka.DialLeader(context.Background(), "tcp", broker, topic, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读回验证连接失败: %v\n", err)
		return
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Seek(fromOffset, io.SeekStart); err != nil {
		fmt.Fprintf(os.Stderr, "读回验证定位失败: %v\n", err)
		return
	}
	// Conn.ReadMessage 以 minBytes 为参数，超时通过读截止时间控制
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		fmt.Fprintf(os.Stderr, "读回验证设置超时失败: %v\n", err)
		return
	}
	msg, err := conn.ReadMessage(1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读回验证超时（不影响已发送内容），可手动消费确认。\n")
		return
	}
	fmt.Printf("已从 Kafka 读回本轮日志（offset=%d），接入成功：\n  %s\n", msg.Offset, msg.Value)
}

// ---------------------------------------------------------------------------
// 演示辅助
// ---------------------------------------------------------------------------

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func firstOf(list string) string {
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return list
}

func reachable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
