package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryLimit(t *testing.T) {
	write := func(t *testing.T, rel, content string) string {
		t.Helper()
		root := t.TempDir()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	tests := []struct {
		name, rel, content string
		want               int64
	}{
		{"v2 limit", "memory.max", "536870912\n", 512 << 20},
		{"v2 unlimited", "memory.max", "max\n", 0},
		{"v1 limit", "memory/memory.limit_in_bytes", "1073741824\n", 1 << 30},
		{"v1 unlimited", "memory/memory.limit_in_bytes", "9223372036854771712\n", 0},
		{"garbage", "memory.max", "lots", 0},
	}
	for _, tt := range tests {
		if got := memoryLimit(write(t, tt.rel, tt.content), ""); got != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, got, tt.want)
		}
	}
	if got := memoryLimit(t.TempDir(), ""); got != 0 {
		t.Errorf("no cgroup files: %d", got)
	}
	// The process's own cgroup wins over an unlimited root.
	root := write(t, "memory/memory.limit_in_bytes", "9223372036854771712\n")
	if err := os.MkdirAll(filepath.Join(root, "memory", "job", "kempt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "job", "kempt", "memory.limit_in_bytes"), []byte("268435456"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := memoryLimit(root, "12:cpu,cpuacct:/job\n9:memory:/job/kempt\n"); got != 256<<20 {
		t.Errorf("own v1 cgroup: %d MiB", got>>20)
	}
	v2 := write(t, "app/memory.max", "1073741824")
	if got := memoryLimit(v2, "0::/app\n"); got != 1<<30 {
		t.Errorf("own v2 cgroup: %d MiB", got>>20)
	}
}

func TestAnalysisSlots(t *testing.T) {
	tests := []struct {
		want  int
		limit int64
		slots int
	}{
		{2, 0, 2},
		{2, 512 << 20, 1}, // Railway Free: one spreadsheet at a time
		{2, 400 << 20, 1},
		{2, 1 << 30, 2},
		{4, 1 << 30, 2},
		{4, 8 << 30, 4},
	}
	for _, tt := range tests {
		if got := analysisSlots(tt.want, tt.limit); got != tt.slots {
			t.Errorf("analysisSlots(%d, %d MiB) = %d, want %d", tt.want, tt.limit>>20, got, tt.slots)
		}
	}
	s, err := New(Config{Demo: true, MemoryLimit: 512 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if cap(s.analyses) != 1 {
		t.Errorf("demo server at 512 MiB reads %d spreadsheets at once", cap(s.analyses))
	}
}
