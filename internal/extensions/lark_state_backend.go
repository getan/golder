package extensions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/getan/golder/extension"
	"github.com/getan/golder/internal/seatbelt"
)

var errStateInUse = errors.New("extension login state is in use by an active operation or an older persistent connection; wait for the operation or disconnect the older connection, without resetting login")

// The key and lease are host-only; model arguments and extension environment
// values cannot bypass prepareCommand's state lock.
type stateLeaseKey struct{}
type stateOperationLease struct {
	path   string
	exited <-chan struct{}
	unlock func()
}

type larkNotDispatchedError struct{ cause error }

func (e *larkNotDispatchedError) Error() string {
	return "Lark operation was not dispatched: " + e.cause.Error()
}
func (e *larkNotDispatchedError) Unwrap() error { return e.cause }

func callScopedLarkState(c SourceConfig) bool {
	return c.Runtime == "" && c.Transport == "stdio" && c.StateDir && c.stateHome != "" &&
		filepath.Base(expandPath(c.Command)) == executableName("golder-lark")
}

// A logical connection owns the reviewed catalog, not an idle child process.
// Every operation starts one helper under the same cross-process exclusive
// lease used by setup/reset. No credentials are copied to per-session HOMEs.
type larkStateBackend struct {
	life    context.Context
	cancel  context.CancelFunc
	cfg     SourceConfig
	check   func() error
	open    func(context.Context, context.Context) (backend, error)
	turn    chan struct{}
	catalog Catalog // protected by turn, including fresh per-call schema checks
}

func newLarkStateBackend(life context.Context, c SourceConfig, cwd string, logs *logBuffer, dirty func(), check func() error) *larkStateBackend {
	life, cancel := context.WithCancel(life)
	return &larkStateBackend{life: life, cancel: cancel, cfg: c, check: check, turn: make(chan struct{}, 1),
		open: func(ctx, childLife context.Context) (backend, error) {
			return openBackend(ctx, childLife, c, cwd, logs, dirty, nil)
		}}
}

func (b *larkStateBackend) operation(ctx context.Context, report func(extension.Progress), run func(context.Context, backend) error) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(b.life, cancel)
	defer stop()
	notDispatched := func(err error) error { return &larkNotDispatchedError{cause: err} }
	if err := b.life.Err(); err != nil {
		return notDispatched(err)
	}
	select {
	case b.turn <- struct{}{}:
		defer func() { <-b.turn }()
	case <-ctx.Done():
		return notDispatched(ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return notDispatched(err)
	}
	if _, err := seatbelt.NewExtensionState(b.cfg.stateHome); err != nil {
		return notDispatched(err)
	}
	if err := privateStateParent(b.cfg.stateHome); err != nil {
		return notDispatched(err)
	}
	path := filepath.Join(filepath.Dir(b.cfg.stateHome), ".lock")
	unlock, err := lockState(path)
	if errors.Is(err, errStateInUse) {
		if report != nil {
			report(extension.Progress{Message: "Waiting for another Lark operation to release shared login state…"})
		}
		unlock, err = lockStateContext(ctx, path)
	}
	if err != nil {
		return notDispatched(err)
	}
	var once sync.Once
	lease := &stateOperationLease{path: path, unlock: func() { once.Do(unlock) }}
	if err := b.life.Err(); err != nil {
		lease.unlock()
		return notDispatched(err)
	}
	if err := ctx.Err(); err != nil {
		lease.unlock()
		return notDispatched(err)
	}
	// A queued call may outlive the original review snapshot. Recheck admission,
	// executable/dependency fingerprints and source identity before any child.
	if err := b.check(); err != nil {
		lease.unlock()
		return notDispatched(err)
	}
	// Cancellation of the caller goes through MCP so the helper can stop/reap
	// its native CLI. Do not SIGKILL the helper before its request drains.
	childLife, childCancel := context.WithCancel(context.WithValue(context.Background(), stateLeaseKey{}, lease))
	initCtx, stopInit := context.WithTimeout(ctx, timeout(b.cfg.StartupTimeout, 15*time.Second))
	inner, err := b.open(initCtx, childLife)
	stopInit()
	if err != nil {
		childCancel() // No business call was sent during initialization.
		finishStateLease(lease, func() {})
		return notDispatched(err)
	}
	defer func() {
		closeErr := closeLarkStateOperation(inner, lease, childCancel)
		if err == nil {
			err = closeErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return notDispatched(err)
	}
	return run(ctx, inner)
}

// Retain the lease if a helper has not confirmed exit. Its own bounded CLI
// cancellation/timeout can drain after the caller returns; another writer must
// not enter merely because a socket disappeared or our two-second wait ended.
func finishStateLease(lease *stateOperationLease, cleanup func()) bool {
	finish := func() { cleanup(); lease.unlock() }
	if lease.exited != nil {
		select {
		case <-lease.exited:
		default:
			go func() { <-lease.exited; finish() }()
			return false
		}
	}
	finish()
	return true
}

func closeLarkStateOperation(inner backend, lease *stateOperationLease, cancel context.CancelFunc) error {
	if child, ok := inner.(*mcpBackend); ok && child.wait != nil {
		if child.session != nil {
			_ = child.session.Close() // closes stdin; the server drains its handlers
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-child.wait:
		case <-timer.C:
		}
		cleanup := func() { cancel(); _ = child.Close() }
		if !finishStateLease(lease, cleanup) {
			return errors.New("Lark helper is still draining; login state remains exclusively locked until exit. Do not replay an operation whose outcome is unknown")
		}
		return nil
	}
	err := inner.Close()
	cancel()
	finishStateLease(lease, func() {})
	return err
}

func (b *larkStateBackend) Catalog(ctx context.Context) (cat Catalog, err error) {
	err = b.operation(ctx, nil, func(ctx context.Context, inner backend) error {
		var err error
		cat, err = inner.Catalog(ctx)
		if err == nil {
			b.catalog = cat
		}
		return err
	})
	return cat, err
}

func (b *larkStateBackend) Call(ctx context.Context, call extension.Call, report func(extension.Progress)) (result extension.Result, err error) {
	err = b.operation(ctx, report, func(ctx context.Context, inner backend) error {
		cat, err := inner.Catalog(ctx)
		if err != nil {
			return &larkNotDispatchedError{cause: err}
		}
		matched := false
		for _, expected := range b.catalog.Tools {
			if expected.Name != call.Tool {
				continue
			}
			for _, actual := range cat.Tools {
				if actual.Name == call.Tool && sameSchema(actual.InputSchema, expected.InputSchema) {
					matched = true
				}
			}
		}
		if !matched {
			return &larkNotDispatchedError{cause: fmt.Errorf("tool %q changed; refresh the catalog before retrying", call.Tool)}
		}
		result, err = inner.Call(ctx, call, report) // exactly once; never replay
		return err
	})
	return result, err
}

func (b *larkStateBackend) GetPrompt(ctx context.Context, name string, args map[string]string) (text string, err error) {
	err = b.operation(ctx, nil, func(ctx context.Context, inner backend) error {
		var err error
		text, err = inner.GetPrompt(ctx, name, args)
		return err
	})
	return text, err
}

func (b *larkStateBackend) ReadResource(ctx context.Context, uri string) (text string, err error) {
	err = b.operation(ctx, nil, func(ctx context.Context, inner backend) error {
		var err error
		text, err = inner.ReadResource(ctx, uri)
		return err
	})
	return text, err
}

func (b *larkStateBackend) Close() error { b.cancel(); return nil }
