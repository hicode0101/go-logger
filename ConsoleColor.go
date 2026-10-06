package logger

import (
	"fmt"
)

type ConsoleColor uint8

const (
	Black ConsoleColor = iota + 30
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	White
	BrightBlack ConsoleColor = iota + 82
	BrightRed
	BrightGreen
	BrightYellow
	BrightBlue
	BrightMagenta
	BrightCyan
	BrightWhite
)

func (_self ConsoleColor) Format(input string) string {

	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", uint8(_self), input)

}
