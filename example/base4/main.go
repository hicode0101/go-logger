// 示例四（base4）：base3 的 ES 形态 —— JSON 行日志接入 Elasticsearch。
//
// 与 base3 的唯一差别是多一路输出：日志除写本地 JSON 文件外，同一条 JSON 行
// 经 es-sink 攒批提交 Elasticsearch 的 _bulk。接入代码全部在 es.go（字节流
// Sink 扩展模式，经 RegisterSink 注册），本文件不出现任何 ES 客户端细节——
// 只需在 Sinks 里声明一路 {Type: "es", Format: "json", Options: {url, index}}。
//
// 运行方式（在 example 目录下）：
//
//	go run ./base4
//
// 前置条件：本地 Elasticsearch（默认 http://localhost:9200），例如：
//
//	docker run -d --name es -p 9200:9200 \
//	  -e discovery.type=single-node -e xpack.security.enabled=false \
//	  docker.elastic.co/elasticsearch/elasticsearch:8.15.0
//
// 查询验证：
//
//	curl 'localhost:9200/base4-logs/_search?pretty&size=5'
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/hicode0101/go-logger"
)

const (
	esURL   = "http://localhost:9200" // ES 集群地址
	esIndex = "base4-logs"            // 固定索引名（按天分索引需 Core 模式，见 example/elasticsearch）
)

func main() {
	// 预检集群可达性，给出友好的启动指引
	if !esReachable(esURL) {
		fmt.Fprintf(os.Stderr, "无法连接 Elasticsearch %s。\n请先启动本地实例，例如：\n"+
			"  docker run -d --name es -p 9200:9200 \\\n"+
			"    -e discovery.type=single-node -e xpack.security.enabled=false \\\n"+
			"    docker.elastic.co/elasticsearch/elasticsearch:8.15.0\n", esURL)
		os.Exit(1)
	}
	before := esCount(esURL, esIndex)

	// ---------------------------------------------------------------------------
	// 正式演示配置：与 base3 相同的 JSON 行，外加一路 "es"（文件兜底 + ES 双写）
	// BufferSizeKB=0：es-sink 内部已攒批，不再叠加缓冲
	// 在 es.go 的 init 初始化方法中 logger.RegisterSink 自动注册，对业务代码几乎零入侵
	// ---------------------------------------------------------------------------
	fmt.Printf("========== JSON 日志接入 Elasticsearch（%s，索引 %s） ==========\n", esURL, esIndex)
	_ = logger.Init(logger.Config{
		Level: "debug",
		Sinks: []logger.SinkConfig{
			{Type: "file", Format: "json", Path: "logs/base4-demo.log", // 本地兜底：ES 不可用时日志仍落盘
				MaxSizeMB: 1, MaxBackups: 3, MaxAgeDays: 7, LocalTime: true},
			{Type: "es", Format: "json", BufferSizeKB: 0,
				Options: map[string]string{"url": esURL, "index": esIndex}},
		},
	})

	logger.Debug("Debug：开发期细节，生产环境会被级别过滤")
	logger.Info("Info：业务关键节点打点 user=%s", "Tom")
	logger.Warn("Warn：可自愈的异常")
	logger.Error("Error：error 及以上级别自动附带调用堆栈")
	logger.ErrorWithErr("读取配置失败", errors.New("file not found"))

	httpLog := logger.Named("http")
	httpLog.Info("listening on :8080")
	_ = logger.Sync()
	_ = logger.Close() // 排空 es-sink 攒批并结束后台协程，之后即可查询验证

	// ES 是近实时（NRT）引擎：写入后默认约 1s 自动 refresh 才可被搜索。
	// demo 需要立即查询验证，故显式触发 refresh（实现见 es.go，生产勿频繁调用）。
	esRefresh(esURL, esIndex)

	after := esCount(esURL, esIndex)
	fmt.Printf("已索引 %d 条日志（集群 %d → %d 条）。\n", after-before, before, after)
	if sample := esSampleDoc(esURL, esIndex); sample != "" {
		fmt.Println("其中一条文档内容（与本地 JSON 文件行完全一致）：")
		fmt.Println(sample)
	}
	fmt.Printf("查询验证：curl '%s/%s/_search?pretty&size=5'\n", esURL, esIndex)
}
