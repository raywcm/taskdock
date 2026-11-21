// Package history reconciles lost sessions and enforces retention independently of PTY brokers.
package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/raywcm/taskdock/internal/ipc"
	"github.com/raywcm/taskdock/internal/store"
	"github.com/raywcm/taskdock/internal/task"
)

// Ensure starts at most one effective worker per state directory. A racing
// startup is harmless: the worker itself owns the exclusive history lock.
func Ensure(repository *store.JSON) error {
	pending, err := pendingWork(context.Background(), repository)
	if pending {
		return errors.Join(err, StartWorker(repository))
	}
	return err
}

func pendingWork(ctx context.Context, repository *store.JSON) (bool, error) {
	records, err := repository.List(ctx)
	if err != nil || len(records) == 0 {
		return false, err
	}
	retention, err := repository.HistoryRetention()
	return err != nil || needsWorker(records, retention), err
}

// StartWorker also works before the first record exists. A creator holding
// the catalog lock can start the worker before publishing a task.
func StartWorker(repository *store.JSON) error {
	release, err := repository.HistoryLock()
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return nil
	}
	if err != nil {
		return err
	}
	release()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(repository.Root(), "history.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer null.Close()
	cmd := exec.Command(exe, "--state-dir", repository.Root(), "__history")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func needsWorker(records []task.Record, retention time.Duration) bool {
	for _, r := range records {
		if retention > 0 || r.Runtime == nil || task.CanTransition(r.Runtime.State, task.Failed) {
			return true
		}
	}
	return false
}

// Run keeps reconciling sessions after the frontend exits. No task signals are sent.
func Run(ctx context.Context, state string) error {
	repository, err := store.OpenExisting(state)
	if err != nil {
		return err
	}
	release, err := repository.HistoryLock()
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return nil
	}
	if err != nil {
		return err
	}
	release = sync.OnceFunc(release)
	defer release()
	var lastWarning string
	for {
		pending, err := Sweep(ctx, repository, time.Now())
		if err != nil {
			if !pending {
				return err
			}
			if warning := err.Error(); warning != lastWarning {
				fmt.Fprintln(os.Stderr, warning)
				lastWarning = warning
			}
		} else {
			lastWarning = ""
		}
		if !pending {
			// Coordinate the final work check with task creation and setting
			// writes. Release the worker lock before the catalog lock so Ensure cannot
			// miss a worker that is about to exit after publishing a new task.
			unlock, err := repository.Lock(ctx)
			if err != nil {
				return err
			}
			pending, err := pendingWork(ctx, repository)
			if !pending {
				release()
			}
			unlock()
			if !pending {
				return err
			}
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Sweep reconciles orphaned sessions and expires retained history under the catalog lock.
// A pending result with an error identifies individual sessions to retry on the next pass.
func Sweep(ctx context.Context, repository *store.JSON, now time.Time) (bool, error) {
	unlock, err := repository.Lock(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	records, err := repository.List(ctx)
	if err != nil || len(records) == 0 {
		return false, err
	}
	// Keep recovering dead sessions. Only expiry depends on a readable policy.
	retention, policyErr := repository.HistoryRetention()
	pending := policyErr != nil
	var warnings []error
	if policyErr != nil {
		warnings = append(warnings, policyErr)
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		keep, err := sweepSession(ctx, repository, record, now, retention)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("session %s: %w", record.Spec.ID, err))
		}
		pending = pending || keep
	}
	return pending, errors.Join(warnings...)
}

func sweepSession(ctx context.Context, repository *store.JSON, record task.Record, now time.Time, retention time.Duration) (bool, error) {
	if record.AwaitingBroker(now) {
		return true, nil
	}
	if record.Runtime == nil || task.CanTransition(record.Runtime.State, task.Failed) {
		latest, err := repository.RetireOrphan(ctx, record.Spec.ID, now)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, nil
		}
		if err != nil {
			return true, err
		}
		record = latest
	}
	if retention == 0 {
		return false, nil
	}
	ended := record.Runtime.UpdatedAt
	if finished := record.Runtime.FinishedAt; finished != nil && !finished.IsZero() {
		ended = *finished
	}
	if now.Before(ended.Add(retention)) {
		return true, nil
	}
	release, err := repository.BrokerLock(record.Spec.ID)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	defer release()
	socket, err := ipc.SocketPath(repository.Root(), record.Spec.ID)
	if err != nil {
		return true, err
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return true, errors.New("refusing to remove non-socket history path")
		}
		if err := os.Remove(socket); err != nil {
			return true, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return true, err
	}
	err = repository.Remove(ctx, record.Spec.ID)
	return err != nil, err
}
