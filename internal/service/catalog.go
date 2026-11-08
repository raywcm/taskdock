// Package service is the shared application boundary for CLI and TUI.
package service

import (
	"context"
	"fmt"
	"github.com/raywcm/taskdock/internal/i18n"
	"github.com/raywcm/taskdock/internal/task"
	"strconv"
	"time"
)

type TaskList interface {
	List(context.Context) ([]task.Record, error)
}

func LabelLocale(r task.Record, l i18n.Locale) string {
	state := "not_started"
	if r.Runtime != nil {
		state = string(r.Runtime.State)
	}
	if r.Health == "unreachable" {
		state = "unreachable"
	}
	id := string(r.Spec.ID)
	if len(id) > 8 {
		id = id[:8]
	}
	d := r.Elapsed(time.Now())
	seconds := int64(d.Seconds())
	return fmt.Sprintf("%s  %s  %s  %02d:%02d:%02d", id, strconv.QuoteToGraphic(r.Spec.Name), l.Text(state), seconds/3600, seconds/60%60, seconds%60)
}
