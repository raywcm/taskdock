package metrics

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func processes(ctx context.Context) (map[int]process, error) {
	ctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-U", strconv.Itoa(os.Getuid()), "-o", "pid=,ppid=,time=,rss=,lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	data, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseProcesses(data), nil
}
func parseProcesses(data []byte) map[int]process {
	result := map[int]process{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 9 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		cpu, e3 := cpuTime(fields[2])
		memory, e4 := strconv.ParseUint(fields[3], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue
		}
		result[pid] = process{pid: pid, parent: parent, identity: strings.Join(fields[4:], " "), cpu: cpu, memory: memory * 1024}
	}
	return result
}
func cpuTime(value string) (float64, error) {
	days := 0.0
	if before, after, ok := strings.Cut(value, "-"); ok {
		var err error
		days, err = strconv.ParseFloat(before, 64)
		if err != nil {
			return 0, err
		}
		value = after
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid CPU time")
	}
	total := 0.0
	for _, part := range parts {
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, err
		}
		total = total*60 + v
	}
	return days*86400 + total, nil
}

func processAt(ctx context.Context, pid int) (process, bool) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,time=,rss=,lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	data, err := cmd.Output()
	if err != nil {
		return process{}, false
	}
	p, ok := parseProcesses(data)[pid]
	return p, ok
}
