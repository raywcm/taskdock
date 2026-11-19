package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/raywcm/taskdock/internal/task"
)

func safe(text string) string {
	quoted := strconv.QuoteToGraphic(text)
	return quoted[1 : len(quoted)-1]
}
func fit(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = ansi.Truncate(text, width, "…")
	return text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
}
func right(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = ansi.Truncate(text, width, "…")
	return strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + text
}
func shortID(id task.ID, all []task.Record) string {
	for length := 8; length < len(id); length++ {
		unique := true
		for _, r := range all {
			if r.Spec.ID != id && strings.HasPrefix(string(r.Spec.ID), string(id[:length])) {
				unique = false
				break
			}
		}
		if unique {
			return string(id[:length])
		}
	}
	return string(id)
}
func elapsed(r task.Record, now time.Time) time.Duration { return r.Elapsed(now) }

func duration(d time.Duration) string {
	seconds := int64(d.Seconds())
	if seconds < 0 {
		seconds = 0
	}
	if seconds >= 86400 {
		return fmt.Sprintf("%dd %02d:%02d", seconds/86400, seconds/3600%24, seconds/60%60)
	}
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}
func memory(bytes uint64) string {
	if bytes >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(bytes)/(1<<30))
	}
	if bytes >= 1<<20 {
		return fmt.Sprintf("%.0f MiB", float64(bytes)/(1<<20))
	}
	if bytes >= 1<<10 {
		return fmt.Sprintf("%d KiB", bytes/1024)
	}
	return fmt.Sprintf("%d B", bytes)
}
func displayPath(dir string) string {
	home, err := os.UserHomeDir()
	if err == nil {
		if dir == home {
			dir = "~"
		} else if strings.HasPrefix(dir, home+string(filepath.Separator)) {
			dir = "~" + strings.TrimPrefix(dir, home)
		}
	}
	return safe(dir)
}

type columns struct{ name, id, age, cpu, mem int }

func layout(width int, resources bool) columns {
	c := columns{}
	if width >= 28 {
		c.age = 8
	}
	if width >= 44 {
		c.id = 8
	}
	if resources && width >= 68 {
		c.cpu = 7
		c.mem = 9
	}
	reserved := 0
	for _, v := range []int{c.id, c.age, c.cpu, c.mem} {
		if v > 0 {
			reserved += v + 2
		}
	}
	c.name = max(1, width-reserved)
	return c
}
func (c columns) render(name, id, age, cpu, mem string) string {
	line := fit(name, c.name)
	for _, pair := range []struct {
		value string
		width int
	}{{id, c.id}, {age, c.age}, {cpu, c.cpu}, {mem, c.mem}} {
		if pair.width > 0 {
			line += "  " + right(pair.value, pair.width)
		}
	}
	return line
}
