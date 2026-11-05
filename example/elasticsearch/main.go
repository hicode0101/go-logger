// 示例三：日志接入 Elasticsearch —— 结构化 Core 扩展模式（docs/EXTEND.md 3.2 的可运行版）。
//
// 模式说明：ES 按"文档行"消费日志，实现 logger.Core 接口可直接消费结构化
// Entry + Field，经 logger.NewLogger(core) 包装成与全局 logger 用法完全一致
// 的日志器实例。相比字节流 Sink，Core 能拿到每条事件的完整结构，本示例
// 演示了其中一个独有能力：按天自动路由索引（logs-2025.11.05）。
//
// 生产要点（本示例均已实现，详见 docs/EXTEND.md）：
//   - Write 内同步转换：Entry/Buffer 池化复用，转换后立即拷贝、不可持有指针；
//   - _bulk NDJSON：action 行与文档行交替，文档由内建 JSON 编码器产出；
//   - 批量异步发送：后台协程攒批（500 条 / 3s）；
//   - With 克隆共享发送管线，仅复制必要切片（Core 接口契约）；
//   - 失败打到 stderr；生产化时补退避重试与本地 file sink 降级兜底。
//
// 前置条件：本地 Elasticsearch（默认 http://localhost:9200），例如：
//
//	docker run -d --name es -p 9200:9200 \
//	  -e discovery.type=single-node -e xpack.security.enabled=false \
//	  docker.elastic.co/elasticsearch/elasticsearch:8.15.0
//
// 运行方式（在 example 目录下）：
//
//	go run ./elasticsearch
//	ES_URL=http://es:9200 ES_INDEX=app-logs go run ./elasticsearch
//
// 查询验证：
//
//	curl 'localhost:9200/app-logs-*/_count?pretty'
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hicode0101/go-logger"
)

// esPipeline 发送管线：With 克隆与根核心共享，只复制必要切片。
type esPipeline struct {
	queue     chan esDoc    // 有界队列
	quit      chan struct{} // Close 信号；不关闭 queue，避免与并发 Write 竞争
	dropped   atomic.Uint64
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// esDoc 一条待索引文档：index 在 Write 期按事件时间决定（结构化 Core 独有能力）。
type esDoc struct {
	index string
	doc   []byte // JSON 行（自带结尾 \n，由内建 jsonEncoder 产出）
}

// esCore 结构化 Core：Entry+Field → 文档 → 攒批 → _bulk 索引。
type esCore struct {
	level   *logger.AtomicLevel // 实例独立级别（可用 Set 动态调整）
	enc     logger.Encoder      // 复用内建 JSON 编码器产出文档行
	fields  []logger.Field      // With 累积的上下文字段
	client  *http.Client
	baseURL string
	index   string      // 基础索引名，按天追加 -YYYY.MM.DD 后缀
	p       *esPipeline // 与 With 克隆共享的发送管线
}

// newESCore 构造核心并启动后台批量发送协程。
func newESCore(baseURL, index string) *esCore {
	c := &esCore{
		level:   logger.NewAtomicLevel(logger.InfoLevel),
		enc:     logger.NewJSONEncoder(logger.EncoderConfig{}),
		client:  &http.Client{Timeout: 10 * time.Second},
		baseURL: strings.TrimRight(baseURL, "/"),
		index:   index,
		p: &esPipeline{
			queue: make(chan esDoc, 2048),
			quit:  make(chan struct{}),
		},
	}
	c.p.wg.Add(1)
	go c.sendLoop()
	return c
}

// Enabled 实现 Core：单次原子读。
func (c *esCore) Enabled(lv logger.Level) bool { return c.level.Enabled(lv) }

// Write 实现 Core：编码 → 按事件时间路由索引 → 非阻塞入队。
func (c *esCore) Write(entry *logger.Entry, fields []logger.Field) error {
	if len(c.fields) > 0 {
		// 三段切片强制复制，避免 append 污染 With 持有的底层数组
		fields = append(c.fields[:len(c.fields):len(c.fields)], fields...)
	}
	var buf logger.Buffer
	c.enc.Encode(&buf, entry, fields)
	doc := append([]byte(nil), buf.Bytes()...) // 拷贝：转换完成后不再依赖栈上缓冲
	select {
	case c.p.queue <- esDoc{
		index: fmt.Sprintf("%s-%s", c.index, entry.Time.Format("2006.01.02")),
		doc:   doc,
	}:
	default:
		c.p.dropped.Add(1) // 队列满：丢弃 + 计数，保护业务热路径
	}
	return nil
}

// Sync 实现 Core：同步排空当前积压。
func (c *esCore) Sync() error {
	pending := make([]esDoc, 0, 64)
	for draining := true; draining; {
		select {
		case d := <-c.p.queue:
			pending = append(pending, d)
		default:
			draining = false
		}
	}
	c.bulk(pending)
	return nil
}

// Close 实现 Core：排空剩余 → 结束后台协程（幂等，With 克隆共享同一守卫）。
func (c *esCore) Close() error {
	c.p.closeOnce.Do(func() {
		close(c.p.quit)
		c.p.wg.Wait()
		if d := c.p.dropped.Load(); d > 0 {
			fmt.Fprintf(os.Stderr, "es-core: 运行期间因队列满丢弃 %d 条日志\n", d)
		}
	})
	return nil
}

// With 实现 Core：克隆携带固定字段，共享发送管线（Core 接口契约）。
func (c *esCore) With(fields []logger.Field) logger.Core {
	clone := &esCore{
		level:   c.level,
		enc:     c.enc,
		client:  c.client,
		baseURL: c.baseURL,
		index:   c.index,
		p:       c.p,
	}
	clone.fields = append(clone.fields[:len(clone.fields):len(clone.fields)], fields...)
	return clone
}

// sendLoop 后台攒批：500 条或 3s 触发一次 _bulk。
func (c *esCore) sendLoop() {
	defer c.p.wg.Done()
	const maxBatch = 500
	batch := make([]esDoc, 0, maxBatch)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case d := <-c.p.queue:
			batch = append(batch, d)
			if len(batch) >= maxBatch {
				c.bulk(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			c.bulk(batch)
			batch = batch[:0]
		case <-c.p.quit: // 排空剩余队列后退出
			for {
				select {
				case d := <-c.p.queue:
					batch = append(batch, d)
				default:
					c.bulk(batch)
					return
				}
			}
		}
	}
}

// bulk 提交一批：_bulk NDJSON（action 行与文档行交替）。
func (c *esCore) bulk(batch []esDoc) {
	if len(batch) == 0 {
		return
	}
	var body bytes.Buffer
	for _, d := range batch {
		fmt.Fprintf(&body, `{"index":{"_index":%q}}`+"\n", d.index)
		body.Write(d.doc) // jsonEncoder 产出的文档行自带结尾 \n
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/_bulk", &body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "es-core: 构造请求失败: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := c.client.Do(req)
	if err != nil {
		// 不能经本 logger 回写（会递归进入本 core），打到 stderr；
		// 生产化时在此加退避重试 + 本地 file sink 降级兜底
		fmt.Fprintf(os.Stderr, "es-core: _bulk 发送 %d 条失败: %v\n", len(batch), err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 || bytes.Contains(data, []byte(`"errors":true`)) {
		fmt.Fprintf(os.Stderr, "es-core: _bulk 部分失败（%s），建议检查映射与版本兼容\n", resp.Status)
	} else {
		fmt.Printf("es-core: _bulk 提交 %d 条 → %s\n", len(batch), resp.Status)
	}
}

func main() {
	baseURL := envOr("ES_URL", "http://localhost:9200")
	index := envOr("ES_INDEX", "app-logs")

	// 预检集群可达性，给出友好的启动指引
	if !reachable(baseURL) {
		fmt.Fprintf(os.Stderr, "无法连接 Elasticsearch %s。\n请先启动本地实例，例如：\n"+
			"  docker run -d --name es -p 9200:9200 \\\n"+
			"    -e discovery.type=single-node -e xpack.security.enabled=false \\\n"+
			"    docker.elastic.co/elasticsearch/elasticsearch:8.15.0\n"+
			"或指定其它地址：ES_URL=http://es:9200 go run ./elasticsearch\n", baseURL)
		os.Exit(1)
	}

	before := countDocs(baseURL, index+"-*")

	// 结构化 Core → 专用日志器：与全局 logger 相互独立，方法完全一致
	esLog := logger.NewLogger(newESCore(baseURL, index))

	fmt.Printf("向 Elasticsearch（%s，索引 %s-YYYY.MM.DD）写日志……\n", baseURL, index)
	esLog.Info("服务启动", logger.String("version", "2.0"), logger.Int("port", 8080))
	esLog.Infow("订单创建", "orderNo", "NO20240101", "amount", 99.5)                // kv 键值对
	esLog.Error("数据库连接超时", logger.String("dsn", "mysql://127.0.0.1:3306/shop")) // 自动附 stacktrace 字段
	esLog.With(logger.String("module", "payment")).Info("支付回调处理完成")             // With 上下文进文档
	esLog.Sync()                                                                // 同步刷出积压
	_ = esLog.Close()                                                           // 排空剩余批次并结束后台协程；Close 幂等（With 克隆共享守卫）

	// ES 是近实时（NRT）引擎：写入后默认约 1s 自动 refresh 才可被搜索。
	// demo 需要立即查询验证，故显式触发 refresh；生产环境请依赖自动 refresh，
	// 不要在日志写入路径上频繁手动刷新（会产生大量小段，影响性能）。
	refreshIndices(baseURL, index+"-*")

	after := countDocs(baseURL, index+"-*")
	fmt.Printf("已索引 %d 条日志（集群 %d → %d 条）。\n", after-before, before, after)

	if sample := sampleDoc(baseURL, index+"-*"); sample != "" {
		fmt.Println("其中一条文档内容（可看到 level/time/caller/msg 及结构化字段）：")
		fmt.Println(sample)
	}
	fmt.Printf("查询验证：curl '%s/%s-*/_search?pretty&size=5'\n", baseURL, index)
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

func reachable(baseURL string) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(baseURL)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true // 任意 HTTP 响应都算可达；开启鉴权的集群请改用带凭据的 client
}

// refreshIndices 强制刷新匹配索引，使刚写入的文档立即可搜（仅 demo 验证用）。
func refreshIndices(baseURL, indexPattern string) {
	req, err := http.NewRequest(http.MethodPost, baseURL+"/"+indexPattern+"/_refresh", nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// countDocs 统计匹配索引的文档数；索引不存在或集群异常返回 0。
func countDocs(baseURL, indexPattern string) int {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/"+indexPattern+"/_count", nil)
	if err != nil {
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Count int `json:"count"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return 0
	}
	return out.Count
}

// sampleDoc 取一条文档（pretty JSON），截断展示。
func sampleDoc(baseURL, indexPattern string) string {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/"+indexPattern+"/_search?size=1&pretty=true", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	const maxLen = 1500
	if len(data) > maxLen {
		return string(data[:maxLen]) + "\n……（已截断）"
	}
	return string(data)
}
