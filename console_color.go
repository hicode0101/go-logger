package logger

import "fmt"

// ConsoleColor ANSI 终端前景色（SGR 转义序列）。
//
// 编码规则：30~37 为普通前景色，90~97 为高亮前景色，
// 输出形如 "\x1b[32m...\x1b[0m" 的转义序列，仅对支持 ANSI 的终端生效
// （Windows 10+ 默认终端、主流 IDE 控制台、Linux/macOS 终端均支持）。
type ConsoleColor uint8

const (
	Black ConsoleColor = iota + 30 // 30 黑色
	Red                            // 31 红色
	Green                          // 32 绿色
	Yellow                         // 33 黄色
	Blue                           // 34 蓝色
	Magenta                        // 35 洋红
	Cyan                           // 36 青色
	White                          // 37 白色

	// BrightBlack 高亮前景色组从 90 重新开始编号。
	// const 块中 iota 在此处已递增到 8，故 8+82=90（高亮黑/灰色）。
	BrightBlack ConsoleColor = iota + 82 // 90 高亮黑
	BrightRed                            // 91 高亮红
	BrightGreen                          // 92 高亮绿
	BrightYellow                         // 93 高亮黄
	BrightBlue                           // 94 高亮蓝
	BrightMagenta                        // 95 高亮洋红
	BrightCyan                           // 96 高亮青
	BrightWhite                          // 97 高亮白
)

// Format 用当前颜色包裹 input，返回带 ANSI 转义序列的字符串。
// 例如 BrightRed.Format("ERROR") 输出终端中显示为红色的 "ERROR"。
func (c ConsoleColor) Format(input string) string {
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", uint8(c), input)
}
