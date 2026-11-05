package logger

import (
	"runtime"
	"sync"
)

// callerPCs 单帧采集的 PC 缓冲：caller 只需要 file:line 一个帧。
// zap 的做法同样是只取 1 个 PC，再由 CallersFrames 解析出 file/line，
// 比全量 runtime.Caller(depth)（内部走更重的 unwind）便宜得多。
var callerPCs [1]uintptr

// getCaller 采集调用位置，返回文件路径与行号（无有效帧时返回 false）。
// 拆分返回避免了 "file:line" 字符串拼接的每条日志分配，
// 编码阶段直接追加 file 与 line 即可（zap EntryCaller 同款设计）。
// skip 语义：0 = getCaller 的调用方，1 = 再上一层，依此类推。
func getCaller(skip int) (string, int, bool) {
	if runtime.Callers(skip+2, callerPCs[:]) == 0 {
		return "", 0, false
	}
	frame, _ := runtime.CallersFrames(callerPCs[:]).Next()
	if frame.PC == 0 {
		return "", 0, false
	}
	return frame.File, frame.Line, true
}

// stackPool 堆栈采集缓冲池：storage 与解析结果解耦，池化避免
// 每条 Error 级日志重新分配 []uintptr（zap internal/stacktrace 同款设计）。
var stackPool = sync.Pool{
	New: func() interface{} {
		return &stackTrace{storage: make([]uintptr, 64)}
	},
}

type stackTrace struct {
	pcs     []uintptr // 有效程序计数器，始终是 storage 的子切片
	storage []uintptr
}

// takeStacktrace 采集并格式化从 skip 层开始的调用堆栈。
// 输出格式与 zap 一致，便于日志平台按同一套规则解析：
//
//	main.main
//		/path/main.go:15
//	github.com/x/y.(*T).handle
//		/path/file.go:42
//
// 空行分隔每一帧；runtime 及不可解析帧会被跳过（与 zap 的 trim 行为对齐）。
func takeStacktrace(skip int) string {
	st := stackPool.Get().(*stackTrace)
	st.pcs = st.storage

	// +2：跳过 runtime.Callers 自身与 takeStacktrace 本身，skip=0 即
	// "takeStacktrace 的直接调用方"（log.log 内部调用点），与 getCaller 语义一致。
	numFrames := runtime.Callers(skip+2, st.pcs)
	if numFrames == 0 {
		stackPool.Put(st)
		return ""
	}

	buf := getBuffer()
	frames := runtime.CallersFrames(st.pcs[:numFrames])
	for i := 0; i < numFrames; i++ {
		frame, more := frames.Next()
		if frame.PC == 0 || frame.File == "" {
			continue
		}
		// 跳过 runtime 包内部帧（如 runtime.goexit 前置帧）
		if frame.Function == "" && frame.File == "runtime" {
			continue
		}
		buf.AppendString(frame.Function)
		buf.AppendByte('\n')
		buf.AppendByte('\t')
		buf.AppendString(frame.File)
		buf.AppendByte(':')
		buf.AppendInt(int64(frame.Line))
		if more {
			buf.AppendByte('\n')
		}
	}
	out := string(buf.Bytes())
	putBuffer(buf)
	stackPool.Put(st)
	return out
}
