// es.go —— base4 的全部 Elasticsearch 接入代码（字节流 Sink 扩展模式）。
//
// main.go 不出现任何 ES 细节：本文件经 init() 调用 logger.RegisterSink("es", …)
// 注册工厂后，main.go 只需在 Sinks 配置里声明一路
//
//	{Type: "es", Format: "json", Options: {"url": …, "index": …}}
//
// 即完成接入。分工：JSON 编码由 go-logger 内建编码器完成（Format: "json"），
// 本文件只负责"字节行 → 攒批 → POST /_bulk"。
//
// 生产要点（docs/EXTEND.md 契约）：
//   - 拷贝再入队：Write 收到的 []byte 来自池化缓冲，调用方随即复用，不可持有；
//   - 有界队列非阻塞：队列满丢弃并计数，绝不阻塞业务调用方；
//   - 批量异步发送：后台协程攒批（500 条 / 3s）提交 _bulk；
//   - Sync/Close 保证 flush；发送失败打到 stderr（不能经本 logger 回写，会递归）。
//
// 与 example/elasticsearch（结构化 Core 模式）的差异：字节流 Sink 拿不到
// Entry 结构，无法按事件时间路由按天索引，故使用固定索引名；需要按天分索引
// 时参考该示例改用 Core 模式。
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

// esSink 字节流 Sink：非阻塞入队 + 后台攒批异步 _bulk。
type esSink struct {
	url       string // 集群地址，如 http://localhost:9200
	index     string // 目标索引名（固定，不含按天后缀）
	client    *http.Client
	queue     chan []byte   // 有界队列
	quit      chan struct{} // Close 信号；不关闭 queue，避免与并发 Write 竞争
	dropped   atomic.Uint64
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// 注册 Sink 工厂（扩展方约定放在包 init 中）。
func init() {
	logger.RegisterSink("es", newESSink)
}

// newESSink Sink 工厂：url/index 经 SinkConfig.Options 传入，由使用方在配置中
// 给出，扩展方与使用方解耦（主模块零依赖、扩展零侵入）。
func newESSink(cfg logger.SinkConfig) (logger.Sink, error) {
	url := cfg.Options["url"]
	if url == "" {
		url = "http://localhost:9200"
	}
	index := cfg.Options["index"]
	if index == "" {
		index = "app-logs"
	}
	s := &esSink{
		url:    strings.TrimRight(url, "/"),
		index:  index,
		client: &http.Client{Timeout: 10 * time.Second},
		queue:  make(chan []byte, 2048),
		quit:   make(chan struct{}),
	}
	s.wg.Add(1)
	go s.sendLoop()
	return s, nil
}

// Write 实现 Sink：拷贝、非阻塞入队。
func (s *esSink) Write(p []byte) (int, error) {
	select {
	case <-s.quit: // 已 Close：丢弃（Close 与并发 Write 竞争时尽力而为）
		return len(p), nil
	default:
	}
	b := make([]byte, len(p)) // 关键：拷贝——p 来自池化缓冲，调用方随即复用
	copy(b, p)
	select {
	case s.queue <- b: // 非阻塞入队（b 自带结尾 \n，可直接作 _bulk 文档行）
	default:
		s.dropped.Add(1) // 队列满：丢弃 + 计数，保护业务热路径
	}
	return len(p), nil
}

// sendLoop 后台攒批：500 条或 3s 触发一次 _bulk。
func (s *esSink) sendLoop() {
	defer s.wg.Done()
	const maxBatch = 500
	batch := make([][]byte, 0, maxBatch)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case b := <-s.queue:
			batch = append(batch, b)
			if len(batch) >= maxBatch {
				s.bulk(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			s.bulk(batch)
			batch = batch[:0]
		case <-s.quit: // 排空剩余队列后退出
			for {
				select {
				case b := <-s.queue:
					batch = append(batch, b)
				default:
					s.bulk(batch)
					return
				}
			}
		}
	}
}

// bulk 提交一批：_bulk NDJSON（action 行与文档行交替）。
func (s *esSink) bulk(batch [][]byte) {
	if len(batch) == 0 {
		return
	}
	var body bytes.Buffer
	for _, doc := range batch {
		fmt.Fprintf(&body, `{"index":{"_index":%q}}`+"\n", s.index)
		body.Write(doc) // go-logger JSON 编码器产出，自带结尾 \n
	}
	req, err := http.NewRequest(http.MethodPost, s.url+"/_bulk", &body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "es-sink: 构造请求失败: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := s.client.Do(req)
	if err != nil {
		// 不能经本 logger 回写（会递归进入本 sink），打到 stderr；
		// 生产化时在此加退避重试 + 本地 file sink 降级兜底
		fmt.Fprintf(os.Stderr, "es-sink: _bulk 发送 %d 条失败: %v\n", len(batch), err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 || bytes.Contains(data, []byte(`"errors":true`)) {
		fmt.Fprintf(os.Stderr, "es-sink: _bulk 部分失败（%s），建议检查映射与版本兼容\n", resp.Status)
	} else {
		fmt.Printf("es-sink: _bulk 提交 %d 条 → %s\n", len(batch), resp.Status)
	}
}

// Sync 实现 Sink：同步排空当前积压。
func (s *esSink) Sync() error {
	pending := make([][]byte, 0, 64)
	for draining := true; draining; {
		select {
		case b := <-s.queue:
			pending = append(pending, b)
		default:
			draining = false
		}
	}
	s.bulk(pending)
	return nil
}

// Close 实现 Sink：排空 → 结束后台协程 → 上报丢弃统计（幂等）。
func (s *esSink) Close() error {
	s.closeOnce.Do(func() {
		close(s.quit)
		s.wg.Wait()
		if d := s.dropped.Load(); d > 0 {
			fmt.Fprintf(os.Stderr, "es-sink: 运行期间因队列满丢弃 %d 条日志\n", d)
		}
	})
	return nil
}

// ---------------------------------------------------------------------------
// 验证辅助（供 main.go 调用；与发送链路无共享状态）
// ---------------------------------------------------------------------------

// esReachable 预检集群可达性。
func esReachable(baseURL string) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(baseURL)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// esRefresh 强制刷新匹配索引，使刚写入的文档立即可搜（仅 demo 验证用；
// ES 近实时：写入后默认约 1s 自动 refresh。生产请依赖自动 refresh，
// 不要在日志写入路径上频繁手动刷新，会产生大量小段影响性能）。
func esRefresh(baseURL, indexPattern string) {
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

// esCount 统计匹配索引的文档数；索引不存在或集群异常返回 0。
func esCount(baseURL, indexPattern string) int {
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

// esSampleDoc 取一条文档（pretty JSON），截断展示。
func esSampleDoc(baseURL, indexPattern string) string {
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
	const maxLen = 1200
	if len(data) > maxLen {
		return string(data[:maxLen]) + "\n……（已截断）"
	}
	return string(data)
}
