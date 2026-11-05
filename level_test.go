package logger

import (
	"sync"
	"testing"
)

// TestParseLevel 覆盖全部合法级别与大小写/别名/非法值分支。
func TestParseLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    Level
		wantErr bool
	}{
		{"debug", DebugLevel, false},
		{"DEBUG", DebugLevel, false},
		{"Info", InfoLevel, false},
		{"warn", WarnLevel, false},
		{"warning", WarnLevel, false}, // 别名
		{"ERROR", ErrorLevel, false},
		{"dpanic", DPanicLevel, false},
		{"Panic", PanicLevel, false},
		{"fatal", FatalLevel, false},
		{"", InfoLevel, true},
		{"not-a-level", InfoLevel, true},
		{"infooo", InfoLevel, true},
	}
	for _, c := range cases {
		got, err := ParseLevel(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseLevel(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestLevelString 验证 String/CapitalString 输出。
func TestLevelString(t *testing.T) {
	for lvl := DebugLevel; lvl <= FatalLevel; lvl++ {
		if lvl.String() == "" || lvl.CapitalString() == "" {
			t.Errorf("级别 %d 的字符串表示为空", lvl)
		}
	}
	if DebugLevel.String() != "debug" {
		t.Errorf("DebugLevel.String() = %q", DebugLevel.String())
	}
	if DebugLevel.CapitalString() != "DEBUG" {
		t.Errorf("DebugLevel.CapitalString() = %q", DebugLevel.CapitalString())
	}
	// 越界级别不 panic，给出可读表示
	if s := Level(99).String(); s != "Level(99)" {
		t.Errorf("越界级别表示 = %q", s)
	}
}

// TestLevelEnabled 验证级别比较语义：lv >= l 即启用。
func TestLevelEnabled(t *testing.T) {
	if !InfoLevel.Enabled(InfoLevel) || !InfoLevel.Enabled(ErrorLevel) {
		t.Error("同级别与更高级别应启用")
	}
	if InfoLevel.Enabled(DebugLevel) {
		t.Error("更低级别不应启用")
	}
}

// TestAtomicLevel 验证动态级别的原子读写。
func TestAtomicLevel(t *testing.T) {
	al := NewAtomicLevel(InfoLevel)
	if al.Level() != InfoLevel {
		t.Fatalf("初始级别应为 info，实际 %v", al.Level())
	}
	if !al.Enabled(WarnLevel) || !al.Enabled(InfoLevel) {
		t.Error("info 级别开关：info/warn 应启用")
	}
	if al.Enabled(DebugLevel) {
		t.Error("info 级别下 debug 不应启用")
	}
	al.Set(DebugLevel)
	if al.Enabled(DebugLevel) {
		return // 调整后 debug 启用，正确
	}
	t.Error("调成 debug 后 debug 应启用")
}

// TestAtomicLevelConcurrent 并发读写级别的竞态检查（go test -race 覆盖）。
func TestAtomicLevelConcurrent(t *testing.T) {
	al := NewAtomicLevel(InfoLevel)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				if i%2 == 0 {
					if j%2 == 0 {
						al.Set(DebugLevel)
					} else {
						al.Set(FatalLevel)
					}
				} else {
					_ = al.Enabled(DebugLevel)
					_ = al.Level()
				}
			}
		}(i)
	}
	wg.Wait()
}
