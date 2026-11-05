package logger

import (
	"strconv"
	"sync"
	"time"
)

// bufSize 池中缓冲区的初始容量：按典型单条日志（时间+级别+caller+消息+少量字段）
// 约 200~500 字节设计，1KiB 起步，超出自动扩容（由底层 slice append 语义保证）。
// 借鉴 zap buffer 包的 _size=1024 经验值。
const bufSize = 1024

// bufferPool 缓冲池。每条日志编码复用同一块内存，消除了
// "每条日志一次 string 拼接 + 一次 fmt.Sprintf" 带来的分配开销，
// 这是 zap 高性能的第一根支柱（sync.Pool + []byte 直写）。
var bufferPool = sync.Pool{
	New: func() interface{} {
		return &Buffer{bs: make([]byte, 0, bufSize)}
	},
}

// getBuffer 从池中取出重置后的缓冲区。
func getBuffer() *Buffer {
	b := bufferPool.Get().(*Buffer)
	b.bs = b.bs[:0]
	return b
}

// putBuffer 归还缓冲区；容量异常膨胀（如单条超大日志撑大过缓冲）时丢弃，
// 避免大缓冲长期驻留池中造成内存浪费。
func putBuffer(b *Buffer) {
	if cap(b.bs) > 64*bufSize { // 超过 64KiB 的缓冲不入池
		return
	}
	bufferPool.Put(b)
}

// Buffer 字节缓冲区，围绕 []byte 直写封装 strconv 的零分配格式化函数。
// 与标准库 bytes.Buffer 相比：不做读写分离、不持有锁、
// Append 系列方法直接在切片上追加，热路径零额外分配。
type Buffer struct {
	bs []byte
}

// Len 返回当前字节数。
func (b *Buffer) Len() int { return len(b.bs) }

// Bytes 返回内部切片，仅供输出使用，调用方不应修改或长期持有。
func (b *Buffer) Bytes() []byte { return b.bs }

// Reset 清空内容（保留容量，供复用）。
func (b *Buffer) Reset() { b.bs = b.bs[:0] }

// AppendByte 追加单个字节。
func (b *Buffer) AppendByte(v byte) { b.bs = append(b.bs, v) }

// AppendString 追加字符串，零分配（仅可能的切片扩容）。
func (b *Buffer) AppendString(s string) { b.bs = append(b.bs, s...) }

// AppendInt 追加十进制整数，底层 strconv.AppendInt 直接写入切片。
func (b *Buffer) AppendInt(v int64) { b.bs = strconv.AppendInt(b.bs, v, 10) }

// AppendUint 追加十进制无符号整数。
func (b *Buffer) AppendUint(v uint64) { b.bs = strconv.AppendUint(b.bs, v, 10) }

// AppendFloat 追加浮点数（最短表示），NaN/Inf 需调用方按 JSON 语义处理。
func (b *Buffer) AppendFloat(v float64, bitSize int) {
	b.bs = strconv.AppendFloat(b.bs, v, 'f', -1, bitSize)
}

// AppendBool 追加布尔值。
func (b *Buffer) AppendBool(v bool) { b.bs = strconv.AppendBool(b.bs, v) }

// AppendTime 按 layout 追加时间。
// layout 精确匹配本库内置格式时走手工编码快路径（见 appendTimeLayout），
// 其余 layout 退化为 time.AppendFormat。
func (b *Buffer) AppendTime(t time.Time, layout string) {
	if layout == timeLayoutMs {
		b.bs = appendTimeLayout(b.bs, t)
		return
	}
	b.bs = t.AppendFormat(b.bs, layout)
}

// AppendQuote 追加 Go 语法转义的字符串（栈帧函数名等场景）。
func (b *Buffer) AppendQuote(s string) { b.bs = strconv.AppendQuote(b.bs, s) }

// Write 实现 io.Writer，便于包装成 fmt.Fprint 的目标。
func (b *Buffer) Write(p []byte) (int, error) {
	b.bs = append(b.bs, p...)
	return len(p), nil
}

// timeLayoutMs 本库内置时间格式：2006-01-02 15:04:05.000，精确到毫秒。
// 相比 ISO8601 更紧凑、中文项目可读性更好（沿用旧版格式，保证输出兼容）。
const timeLayoutMs = "2006-01-02 15:04:05.000"

// TimeLayout 返回本库内置的时间格式串（2006-01-02 15:04:05.000）。
// 自定义编码器用它配合 Buffer.AppendTime 走毫秒快路径编码。
func TimeLayout() string { return timeLayoutMs }

// digits 两位数字查表：跳过 strconv 的通用逻辑，一次内存读完成两位数编码。
// 包初始化时由程序生成，与手写 200 项字面量等价且不会抄错。
var digits = func() [100][2]byte {
	var d [100][2]byte
	for i := range d {
		d[i][0] = byte('0' + i/10)
		d[i][1] = byte('0' + i%10)
	}
	return d
}()

// appendTwoDigits 追加两位数字（n < 100）。
func appendTwoDigits(b []byte, n int) []byte {
	return append(b, digits[n][0], digits[n][1])
}

// appendFourDigits 追加四位数字，直接除法展开，避免 strconv 的通用路径。
func appendFourDigits(b []byte, n int) []byte {
	a := n / 1000 % 10
	c := n / 10 % 10
	d := n % 10
	return append(b, byte(a+'0'), byte(n/100%10+'0'), byte(c+'0'), byte(d+'0'))
}

// appendTimeLayout 手工把时间编码为 "2006-01-02 15:04:05.mmm"。
// time.Format 每次调用需解析 layout 并按 layout 逐段匹配（约 100~200ns + 分配），
// 手工编码仅做几次整数除法与查表，实测快 5~10 倍且零分配。
// 同样的手法见 zap 内部对 ISO8601/RFC3339 布局的专用编码器。
func appendTimeLayout(b []byte, t time.Time) []byte {
	year, month, day := t.Date()
	hour, min, sec := t.Clock()
	ms := t.Nanosecond() / 1e6

	b = appendFourDigits(b, year)
	b = append(b, '-')
	b = appendTwoDigits(b, int(month))
	b = append(b, '-')
	b = appendTwoDigits(b, day)
	b = append(b, ' ')
	b = appendTwoDigits(b, hour)
	b = append(b, ':')
	b = appendTwoDigits(b, min)
	b = append(b, ':')
	b = appendTwoDigits(b, sec)
	b = append(b, '.')
	// 毫秒为三位数（000~999），不能走两位数查表
	b = append(b, byte(ms/100+'0'), byte(ms/10%10+'0'), byte(ms%10+'0'))
	return b
}
