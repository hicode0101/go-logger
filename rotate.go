package logger

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 滚动切割文件写入器：gopkg.in/natefinch/lumberjack.v2 的等价内建实现。
//
// 行为对照 lumberjack v2.2.1：
//   - 写入超过 MaxSizeMB 触发切割：当前文件改名为
//     "前缀-2006-01-02T15-04-05.000.扩展名"，新建同名日志文件继续写入；
//   - MaxBackups：历史文件按时间从新到旧保留 N 个，其余删除；
//   - MaxAgeDays：删除修改时间早于 N 天前的历史文件；
//   - Compress：对将保留的历史文件做 gzip 压缩（原文删除）；
//   - 文件被外部移动/删除时惰性重建；
//   - 清理与压缩在独立后台协程（mill）执行，不阻塞写入路径。
// ---------------------------------------------------------------------------

// compressSuffix gzip 压缩文件后缀。
const compressSuffix = ".gz"

// backupTimeFormat 切割备份文件的时间戳格式（lumberjack 同款）。
const backupTimeFormat = "2006-01-02T15-04-05.000"

// defaultMaxSizeMB MaxSizeMB 未配置（<=0）时的兜底值，对齐 lumberjack 默认 100MB。
const defaultMaxSizeMB = 100

// nowFunc 当前时间函数，测试可替换。
var nowFunc = time.Now

// Rotator 滚动切割文件写入器，实现 Sink 接口。
// 零值不可用，须至少设置 Filename（或经 NewRotator 构造）。
type Rotator struct {
	// Filename 日志文件路径（含文件名），如 logs/app.log。
	Filename string
	// MaxSizeMB 单个文件最大体积（MB），超过后触发切割；<=0 取默认 100。
	MaxSizeMB int
	// MaxBackups 最多保留的历史切割文件个数；0 表示不按个数清理。
	MaxBackups int
	// MaxAgeDays 历史切割文件最长保留天数；0 表示不按天数清理。
	MaxAgeDays int
	// Compress 是否对历史切割文件做 gzip 压缩。
	Compress bool
	// LocalTime 切割文件名时间戳使用本地时间（false 用 UTC）。
	LocalTime bool

	mu        sync.Mutex
	file      *os.File
	size      int64
	millCh    chan bool
	startMill sync.Once
}

// NewRotator 按参数创建并打开滚动文件写入器。
func NewRotator(path string, maxSizeMB, maxBackups, maxAgeDays int, compress, localTime bool) (*Rotator, error) {
	r := &Rotator{
		Filename:   path,
		MaxSizeMB:  maxSizeMB,
		MaxBackups: maxBackups,
		MaxAgeDays: maxAgeDays,
		Compress:   compress,
		LocalTime:  localTime,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r, r.openExistingOrNew(0)
}

// Write 实现 Sink。锁粒度只覆盖"判断切割 + 写文件"，
// 清理压缩在 mill 后台协程完成，写入路径不被清理开销拖慢。
func (r *Rotator) Write(p []byte) (n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	writeLen := int64(len(p))
	if writeLen > r.maxBytes() {
		return 0, fmt.Errorf("logger: 单条日志 %d 字节超过单文件上限 %d MB，拒绝写入", writeLen, r.MaxSizeMB)
	}

	if r.file == nil {
		if err = r.openExistingOrNew(len(p)); err != nil {
			return 0, err
		}
	}

	if r.size+writeLen > r.maxBytes() {
		if err = r.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// Sync 实现 Sink：滚动写模式下每条日志直写文件，无需额外刷写（空操作，
// 与 lumberjack 行为一致）。启用缓冲写包装时由包装层承担刷盘职责。
func (r *Rotator) Sync() error { return nil }

// Close 实现 Sink：刷写并关闭文件句柄（Windows 下不关闭会导致文件被占用）。
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.close()
}

func (r *Rotator) close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// maxBytes 单文件大小上限（字节），未配置时取默认值。
func (r *Rotator) maxBytes() int64 {
	mb := r.MaxSizeMB
	if mb <= 0 {
		mb = defaultMaxSizeMB
	}
	return int64(mb) * 1024 * 1024
}

// rotate 切割：关闭当前文件 → 改名为带时间戳的备份 → 打开新文件 → 触发清理。
func (r *Rotator) rotate() error {
	if err := r.close(); err != nil {
		return err
	}
	if err := r.openNew(); err != nil {
		return err
	}
	r.mill()
	return nil
}

// openNew 把现存日志文件改名为备份，并打开全新的日志文件。
func (r *Rotator) openNew() error {
	if err := os.MkdirAll(r.dir(), 0o755); err != nil {
		return fmt.Errorf("logger: 创建日志目录失败: %w", err)
	}
	name := r.filename()
	mode := os.FileMode(0o600)
	if info, err := os.Stat(name); err == nil {
		// 现存文件改名为备份文件，保留原权限位
		mode = info.Mode()
		if err := os.Rename(name, r.backupName()); err != nil {
			return fmt.Errorf("logger: 日志文件改名失败: %w", err)
		}
	}
	// 新日志文件只在"确认不存在"时才走到这里，O_TRUNC 兜底防御
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("logger: 创建新日志文件失败: %w", err)
	}
	r.file = f
	r.size = 0
	return nil
}

// openExistingOrNew 打开现存日志文件；不存在或不可写时新建。
func (r *Rotator) openExistingOrNew(_ int) error {
	r.mill() // 顺手触发一次清理（惰性执行，有节流）

	name := r.filename()
	info, err := os.Stat(name)
	if os.IsNotExist(err) {
		return r.openNew()
	}
	if err != nil {
		return fmt.Errorf("logger: 获取日志文件信息失败: %w", err)
	}
	f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0o666)
	if err == nil {
		r.file = f
		r.size = info.Size()
		return nil
	}
	if os.IsPermission(err) {
		return r.openNew()
	}
	return fmt.Errorf("logger: 打开日志文件失败: %w", err)
}

// ---------------------------------------------------------------------------
// 备份文件命名与清理
// ---------------------------------------------------------------------------

// filename 日志文件全路径。
func (r *Rotator) filename() string { return r.Filename }

// dir 日志文件所在目录。
func (r *Rotator) dir() string { return filepath.Dir(r.filename()) }

// backupName 生成的备份文件名：时间戳插在主名与扩展名之间，
// 如 logs/app.log → logs/app-2024-06-01T15-04-05.123.log。
func (r *Rotator) backupName() string {
	name := r.filename()
	ext := filepath.Ext(name)
	prefix := name[:len(name)-len(ext)]
	t := nowFunc()
	if !r.LocalTime {
		t = t.UTC()
	}
	return fmt.Sprintf("%s-%s%s", prefix, t.Format(backupTimeFormat), ext)
}

// mill 请求一次后台清理：channel 缓冲为 1 且非阻塞投递，
// 清理请求并发去重，同一时刻至多一个 mill 协程在干活。
func (r *Rotator) mill() {
	r.startMill.Do(func() { r.millCh = make(chan bool, 1); go r.millRun() })
	select {
	case r.millCh <- true:
	default:
	}
}

// millRun 清理协程主循环。
func (r *Rotator) millRun() {
	for range r.millCh {
		r.millRunOnce()
	}
}

// millRunOnce 执行一轮清理：按个数删除 → 按天数删除 → gzip 压缩剩余。
func (r *Rotator) millRunOnce() {
	if r.MaxBackups == 0 && r.MaxAgeDays == 0 && !r.Compress {
		return
	}

	files, err := r.oldLogFiles()
	if err != nil {
		return
	}

	var compress, remove []logInfo

	// 按个数：文件按时间从新到旧排序，超出配额的删除
	if r.MaxBackups > 0 && r.MaxBackups < len(files) {
		remaining := files[:r.MaxBackups]
		remove = append(remove, files[r.MaxBackups:]...)
		files = remaining
	}

	// 按天数：修改时间早于保留窗口的删除
	if r.MaxAgeDays > 0 {
		cutoff := nowFunc().Add(-24 * time.Hour * time.Duration(r.MaxAgeDays))
		var remaining []logInfo
		for _, f := range files {
			if f.timestamp.Before(cutoff) {
				remove = append(remove, f)
			} else {
				remaining = append(remaining, f)
			}
		}
		files = remaining
	}

	// 压缩：仅对未压缩的历史文件执行
	if r.Compress {
		for _, f := range files {
			if !strings.HasSuffix(f.Name, compressSuffix) {
				compress = append(compress, f)
			}
		}
	}

	dir := r.dir()
	for _, f := range remove {
		if err := os.Remove(filepath.Join(dir, f.Name)); err != nil {
			continue // 文件可能已被并发清理删除，忽略
		}
	}
	for _, f := range compress {
		src := filepath.Join(dir, f.Name)
		dst := src + compressSuffix
		_ = compressLogFile(src, dst)
	}
}

// oldLogFiles 枚举本日志文件的历史备份（含压缩后缀），按时间从新到旧排序。
// 不匹配备份命名规则的文件（如手工创建的副本）被忽略，避免误删。
func (r *Rotator) oldLogFiles() ([]logInfo, error) {
	files, err := os.ReadDir(r.dir())
	if err != nil {
		return nil, fmt.Errorf("logger: 读取日志目录失败: %w", err)
	}
	prefix, ext := r.prefixAndExt()
	var out []logInfo
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if t, err := timeFromName(name, prefix, ext); err == nil {
			out = append(out, logInfo{timestamp: t, Name: name})
			continue
		}
		// 已压缩文件去掉 .gz 后再尝试解析时间戳
		if t, err := timeFromName(strings.TrimSuffix(name, compressSuffix), prefix, ext); err == nil {
			out = append(out, logInfo{timestamp: t, Name: name})
		}
	}
	// 时间戳字典序即时间序，从新到旧排序
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].timestamp.After(out[j-1].timestamp); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// prefixAndExt 返回日志文件主名（仅文件名部分，不含目录）与扩展名。
// oldLogFiles 用它与目录内的文件名做前缀匹配，故必须取 Base。
func (r *Rotator) prefixAndExt() (string, string) {
	name := filepath.Base(r.filename())
	ext := filepath.Ext(name)
	return name[:len(name)-len(ext)], ext
}

// logInfo 备份文件信息。
type logInfo struct {
	timestamp time.Time // 从文件名解析出的切割时间
	Name      string    // 文件名（不含目录）
}

// timeFromName 校验 filename 形如 "<prefix>-<timestamp><ext>" 并解析时间戳。
// 严格校验总长度（时间戳部分长度固定），避免对当前日志文件等非备份名切片越界。
func timeFromName(filename, prefix, ext string) (time.Time, error) {
	tsLen := len(backupTimeFormat)
	if len(filename) != len(prefix)+1+tsLen+len(ext) {
		return time.Time{}, fmt.Errorf("logger: 文件名 %s 长度不符", filename)
	}
	if !strings.HasPrefix(filename, prefix) || !strings.HasSuffix(filename, ext) {
		return time.Time{}, fmt.Errorf("logger: 文件名 %s 不匹配备份规则", filename)
	}
	if filename[len(prefix)] != '-' {
		return time.Time{}, fmt.Errorf("logger: 文件名 %s 缺少时间戳分隔符", filename)
	}
	ts := filename[len(prefix)+1 : len(filename)-len(ext)]
	return time.Parse(backupTimeFormat, ts)
}

// compressLogFile gzip 压缩 src → dst 并删除 src，保留原文件权限位。
func compressLogFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := in.Close(); err != nil {
		return err
	}
	// 压缩成功才删除原文件，失败时保留原文避免数据丢失
	return os.Remove(src)
}
