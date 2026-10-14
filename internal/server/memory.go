package server

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// analysisMemory is roughly what reading one maximum-size demo spreadsheet
// takes at its peak.
const analysisMemory = 512 << 20

// MemoryLimit returns the memory limit of the container the process runs in
// (cgroup v2 memory.max, or v1 memory.limit_in_bytes, for the process's own
// cgroup or the root of the mount), or 0 when there is none or it cannot be
// read.
func MemoryLimit() int64 {
	self, _ := os.ReadFile("/proc/self/cgroup")
	return memoryLimit("/sys/fs/cgroup", string(self))
}

// memoryLimit reads the limit under root. self is /proc/self/cgroup: lines
// "0::/path" (v2) and "N:memory:/path" (v1) name the process's cgroup.
func memoryLimit(root, self string) int64 {
	var paths []string
	for _, line := range strings.Split(self, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || parts[2] == "/" {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			paths = append(paths, filepath.Join(root, parts[2], "memory.max"))
		case strings.Contains(","+parts[1]+",", ",memory,"):
			paths = append(paths, filepath.Join(root, "memory", parts[2], "memory.limit_in_bytes"))
		}
	}
	paths = append(paths, filepath.Join(root, "memory.max"), filepath.Join(root, "memory", "memory.limit_in_bytes"))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || n <= 0 || n >= 1<<60 { // "max", or v1's "unlimited" sentinel
			continue
		}
		return n
	}
	return 0
}

// analysisSlots is how many spreadsheets may be read at once: want, or fewer
// when the memory limit cannot hold that many reads.
func analysisSlots(want int, limit int64) int {
	if limit <= 0 {
		return want
	}
	return max(1, min(want, int(limit/analysisMemory)))
}
