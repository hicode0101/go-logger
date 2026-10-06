# go-logger

基于 [zap](https://github.com/uber-go/zap) + [lumberjack](https://github.com/natefinch/lumberjack) 的开箱即用 Go 日志库：**import 即用**，控制台彩色输出 + 按大小自动滚动切割的日志文件，运行期可动态调整级别。

## 特性

- **开箱即用**：import 后直接调用，无需任何初始化代码
- **双路输出**：控制台（带 ANSI 颜色）+ 滚动文件（纯文本），可独立开关
- **滚动切割**：单文件超限自动切割、保留个数/天数可控、可选 gzip 压缩
- **动态级别**：运行期 `SetLevel` 即时生效，常用于线上临时打开 debug
- **低依赖**：仅依赖 `zap`（高性能日志核心）与 `lumberjack`（文件滚动）两个第三方库

## 安装

```bash
go get github.com/hicode0101/go-logger
```

```go
import "github.com/hicode0101/go-logger"
```

## 快速上手

```go
package main

import (
	"errors"

	"github.com/hicode0101/go-logger"
)

func main() {
	// 不调用 Init 也完全可以：按默认配置输出到控制台 + logs/app.log
	logger.Debug("调试信息")
	logger.Info("用户 %s 登录成功", "Tom")          // 格式串风格
	logger.Warn("磁盘使用率 %v%%", 85.5)
	logger.Error("数据库连接超时")
	logger.ErrorWithErr("读取配置失败", errors.New("file not found"))

	// 需要自定义时：
	cfg := logger.DefaultConfig()
	cfg.Level = "info"                       // 只输出 info 及以上
	cfg.FilePath = "logs/server.log"         // 自定义文件路径
	cfg.MaxSizeMB = 100                      // 单文件最大 100MB
	cfg.MaxBackups = 30                      // 保留 30 个历史文件
	cfg.MaxAgeDays = 15                      // 保留 15 天
	cfg.Compress = true                      // 历史文件 gzip 压缩
	_ = logger.Init(cfg)

	defer logger.Close() // 进程退出前释放日志文件句柄
}
```

控制台输出效果（级别着色，含时间、级别、调用位置）：

```text
2024-06-01 12:30:45.123	INFO	main/main.go:12	用户 Tom 登录成功
2024-06-01 12:30:45.124	ERROR	main/main.go:15	数据库连接超时
goroutine 1 [running]:
...（error 级别自动附带堆栈）
```

## 配置项（Config）

| 字段 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `Level` | string | `debug` | 日志级别：debug / info / warn / error / dpanic / panic / fatal（大小写均可） |
| `Console` | bool | `true` | 是否输出到控制台 |
| `File` | bool | `true` | 是否写入滚动日志文件 |
| `FilePath` | string | `logs/app.log` | 日志文件路径 |
| `MaxSizeMB` | int | `1024` | 单个日志文件最大体积（MB），超过后自动切割 |
| `MaxBackups` | int | `10` | 最多保留的历史日志文件个数 |
| `MaxAgeDays` | int | `7` | 历史日志最长保留天数 |
| `Compress` | bool | `false` | 是否 gzip 压缩历史日志 |
| `Development` | bool | `false` | 开发模式：DPanic 级别直接 panic |

所有字段留零值时自动使用默认值，因此 `logger.Init(logger.DefaultConfig())` 等价于默认行为。

---

## 方法详解（按使用场景）

### Init — 按需定制初始化

**使用场景 1：生产环境只输出 info 级别，日志按 100MB 切割保留一个月**

```go
cfg := logger.DefaultConfig()
cfg.Level = "info"
cfg.MaxSizeMB = 100
cfg.MaxAgeDays = 30
if err := logger.Init(cfg); err != nil {
	panic(err) // 只有 Level 非法才会报错
}
```

**使用场景 2：本地调试只看控制台，不产生日志文件**

```go
cfg := logger.DefaultConfig()
cfg.File = false
_ = logger.Init(cfg)
```

**使用场景 3：运行期整体切换输出目标（如收到 SIGHUP 重载配置）**

```go
// Init 可重复调用，重建前会自动刷写并关闭旧文件句柄，不会泄漏
_ = logger.Init(newCfg)
```

### DefaultConfig — 取默认配置再微调

**使用场景**：只想改一两个字段，不关心其它默认值。

```go
cfg := logger.DefaultConfig()
cfg.FilePath = "/var/log/myapp/app.log"
_ = logger.Init(cfg)
```

### Debug / Info / Warn — 常规三级日志

参数形态统一支持三种写法（`f interface{}, v ...interface{}`）：

| 写法 | 示例 | 输出 |
|---|---|---|
| 纯文本 | `logger.Info("服务启动完成")` | `服务启动完成` |
| fmt 格式串 | `logger.Info("用户 %s 登录，id=%d", "Tom", 1001)` | `用户 Tom 登录，id=1001` |
| 非字符串 | `logger.Debug(12345)` / `logger.Debug(err)` | `12345` / `some error` |

**使用场景 1：请求处理链路打点**

```go
logger.Debug("收到请求 method=%s path=%s", r.Method, r.URL.Path) // 开发期细节
logger.Info("订单 %s 创建成功，金额=%.2f", orderNo, amount)       // 业务关键节点
logger.Warn("接口响应偏慢 cost=%dms", cost)                      // 可 self-heal 的异常
```

**使用场景 2：格式串里没有占位符但传了参数，会自动按 `%v` 追加**

```go
logger.Warn("连接池已满，当前:", n) // 输出：连接池已满，当前: 17
```

### Error — 错误日志（自动附堆栈）

**使用场景**：error 及以上级别自动记录调用堆栈，无需手动传。

```go
logger.Error("支付回调验签失败，orderNo=%s", orderNo)
```

### ErrorWithErr — 带原始 error 字段的错误日志

**使用场景**：底层错误向上传递时，在业务语义上下文记录原始错误；err 会作为独立字段输出，便于日志系统按错误内容聚合检索。

```go
if err := os.ReadFile(path); err != nil {
	logger.ErrorWithErr("读取配置文件失败", err)
	// 输出：... ERROR ... 读取配置文件失败	{"error": "open app.yaml: no such file or directory"}
}
```

err 为 nil 时退化为普通 Error 输出，不会打印空 error 字段。

### Panic — 输出日志后 panic

**使用场景**：不可恢复、但希望被上层 `recover()` 统一兜底（如 HTTP 框架的 recover 中间件）转成 500 的场景。

```go
if conn == nil {
	logger.Panic("数据库连接池初始化失败") // 先写日志，再 panic
}
```

### Fatal — 输出日志后退出进程

**使用场景**：启动阶段的致命错误（端口占用、必需配置缺失），进程无法继续运行。注意 `os.Exit(1)` 会跳过 defer，必要时先 `Sync()`。

```go
listener, err := net.Listen("tcp", ":8080")
if err != nil {
	logger.Sync()
	logger.Fatal("端口监听失败: %v", err)
}
```

### SetLevel / GetLevel — 运行期动态级别

**使用场景 1**：线上灰度/排查问题时临时打开 debug，处理完再关回去，无需重启。

```go
_ = logger.SetLevel("debug") // 立即对控制台和文件同时生效
...排查...
_ = logger.SetLevel("info")
```

**使用场景 2**：读取配置文件里的级别字符串直接应用。

```go
_ = logger.SetLevel(viper.GetString("log.level"))
```

```go
if logger.GetLevel() == zapcore.DebugLevel {
	logger.Debug("高开销的调试信息构建: %+v", bigObject) // 级别过滤前的廉价预判
}
```

### Named — 按模块区分子日志器

**使用场景**：不同模块复用同一份输出配置，但日志中能看出来源模块。

```go
httpLog := logger.Named("http")
dbLog := logger.Named("db")
httpLog.Info("listening on :8080") // ... logger=http	msg=listening on :8080
dbLog.Info("connected")
```

### WithFields — 携带固定业务字段

**使用场景**：链路追踪，把 request id、用户 id 注入本次请求的所有日志。

```go
reqLog := logger.WithFields(map[string]interface{}{
	"requestId": "req-9f8e7d",
	"uid":       10001,
})
reqLog.Info("开始处理下单") // 每条日志都自动带 requestId/uid 字段
reqLog.Warn("库存偏低，sku=%s", sku)
```

### GetLogger — 拿到底层 *zap.Logger

**使用场景**：需要 zap 的高级能力（SugaredLogger、Hook、自定义 Field）时。返回副本已抵消包装函数的 caller 偏移，直接调用行号定位准确。

```go
zlog := logger.GetLogger()
sugar := zlog.Sugar()
sugar.Infow("下单成功", "orderNo", orderNo, "amount", amount) // 结构化键值对
```

### Sync / Close — 进程退出前收尾

**使用场景**：优雅停机时刷写缓冲并释放日志文件句柄（Windows 下不 Close 会导致日志文件被占用）。

```go
func main() {
	...
	// 方式一：只刷缓冲
	defer logger.Sync()
	// 方式二：刷缓冲并关闭文件句柄（推荐）
	defer logger.Close()
}
```

---

## 自定义编码器与颜色

`TimeEncoder`、`ColorLevelEncoder` 与 `ConsoleColor` 均为导出成员，方便在自建 zap logger 时复用：

```go
// ConsoleColor：30~37 普通前景色，90~97 高亮前景色
fmt.Println(logger.BrightRed.Format("FAILED"))      // 终端中显示红色
fmt.Println(logger.BrightGreen.Format("OK"))        // 终端中显示绿色

// 自建 zap core 时复用本库编码器（与库内控制台输出风格完全一致）
cfg := zapcore.EncoderConfig{
	EncodeLevel: logger.ColorLevelEncoder,
	EncodeTime:  logger.TimeEncoder,
	...
}
```

级别颜色映射在 `encoder.go` 的 `levelColorMap` 中登记（DEBUG 绿 / INFO 蓝 / WARN 黄 / ERROR 红，其余兜底洋红），增删改只需编辑这一处。

## 从旧版本升级（破坏性变更对照）

| 旧版 | 新版 | 说明 |
|---|---|---|
| `logger.Error(msg string, e error)` | `logger.ErrorWithErr(msg, err)` | `Error` 改为与其它级别一致的 `(f, v...)` 签名 |
| `logger.ErrorMsg(f, v...)` | `logger.Error(f, v...)` | 去掉冗余的 ErrorMsg |
| `logger.GetZlogs()` | `logger.GetLogger()` | 命名规范化；返回副本已修正 caller 偏移 |
| `go.mod` 里 `github.com/natefinch/lumberjack v2.0.0+incompatible` | `gopkg.in/natefinch/lumberjack.v2 v2.2.1` | 修正依赖到正确维护的模块 |
| `go 1.26.8` | `go 1.25.1` | 修正为实际可用工具链版本 |
| 无 | `logger.Init(Config)` / `SetLevel` / `Close` / `Fatal` / `Named` / `WithFields` | 新增能力 |

模块路径由 `go-logger` 调整为 `github.com/hicode0101/go-logger`，import 路径相应更新。

## 依赖说明

| 依赖 | 用途 | 为什么保留 |
|---|---|---|
| `go.uber.org/zap` | 高性能结构化日志核心 | 核心能力，无同级替代 |
| `gopkg.in/natefinch/lumberjack.v2` | 日志文件滚动切割 | 核心能力，无同级替代 |

已移除旧版本误引入的 `github.com/natefinch/lumberjack v2.0.0+incompatible` 与 `BurntSushi/toml`、`yaml.v2` 等无关间接依赖。
