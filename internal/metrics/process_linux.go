package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func processes(ctx context.Context) (map[int]process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	result := map[int]process{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Getuid() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		p, ok := parseStat(pid, string(data))
		if ok {
			result[pid] = p
		}
	}
	return result, nil
}
func parseStat(pid int, line string) (process, bool) {
	end := strings.LastIndex(line, ") ")
	if end < 0 {
		return process{}, false
	}
	fields := strings.Fields(line[end+2:])
	if len(fields) < 22 {
		return process{}, false
	}
	parent, e1 := strconv.Atoi(fields[1])
	user, e2 := strconv.ParseFloat(fields[11], 64)
	system, e3 := strconv.ParseFloat(fields[12], 64)
	rss, e4 := strconv.ParseInt(fields[21], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || rss < 0 {
		return process{}, false
	}
	// Linux amd64/arm64 expose /proc CPU counters in USER_HZ=100 units.
	return process{pid: pid, parent: parent, identity: fields[19], cpu: (user + system) / 100, memory: uint64(rss) * uint64(os.Getpagesize())}, true
}

func processAt(ctx context.Context, pid int) (process, bool) {
	if ctx.Err() != nil {
		return process{}, false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return process{}, false
	}
	return parseStat(pid, string(data))
}
