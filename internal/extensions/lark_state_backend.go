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
// Every operation starts its own helper. Verified Lark business calls share a
// maintenance lease; unknown CLIs/services retain the setup/reset exclusion.
// No credentials are copied to per-session HOMEs.
type larkStateBackend struct {
	life          context.Context
	cancel        context.CancelFunc
	cfg           SourceConfig
	check         func() error
	open          func(context.Context, context.Context) (backend, error)
	mu            sync.RWMutex
	catalog       Catalog
	concurrentCLI func() bool // host-only eligibility; not an environment/model flag
}

func newLarkStateBackend(life context.Context, c SourceConfig, cwd string, logs *logBuffer, dirty func(), check func() error) *larkStateBackend {
	life, cancel := context.WithCancel(life)
	return &larkStateBackend{life: life, cancel: cancel, cfg: c, check: check,
		concurrentCLI: func() bool { return verifiedConcurrentLarkCLI(c) },
		open: func(ctx, childLife context.Context) (backend, error) {
			return openBackend(ctx, childLife, c, cwd, logs, dirty, nil)
		}}
}

func (b *larkStateBackend) operation(ctx context.Context, plan larkOperationPlan, report func(extension.Progress), run func(context.Context, backend) error) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(b.life, cancel)
	defer stop()
	notDispatched := func(err error) error { return &larkNotDispatchedError{cause: err} }
	if err := b.life.Err(); err != nil {
		return notDispatched(err)
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
	if plan.shared && (b.concurrentCLI == nil || !b.concurrentCLI()) {
		if report != nil {
			report(extension.Progress{Message: "Lark CLI is not in the verified concurrency set; using source-exclusive execution."})
		}
		plan = larkOperationPlan{}
	}
	unlock, err := acquireLarkLease(ctx, b.cfg.stateHome, plan, report)
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
		if closeErr != nil {
			// A failed or cancelled call may also leave the helper draining under
			// the lease. Preserve both facts so the caller does not mistake an
			// unknown outcome for permission to immediately retry.
			err = errors.Join(err, closeErr)
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
		lease.exited = child.wait
		if child.closeInput != nil {
			// Signal EOF to cancel/drain server handlers, but keep consuming
			// stdout until the process exits. ClientSession.Close closes BOTH
			// pipes; a final cleanup response can then cause SIGPIPE, terminating
			// the helper before its native CLI and deferred cleanup finish.
			_ = child.closeInput()
		} else if child.session != nil {
			_ = child.session.Close()
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-child.wait:
		case <-timer.C:
		}
		cleanup := func() { cancel(); _ = child.Close() }
		if !finishStateLease(lease, cleanup) {
			return errors.New("Lark helper is still draining; login state remains exclusively locked against maintenance or conflicting operations until exit. Do not replay an operation whose outcome is unknown")
		}
		if child.waitErr != nil {
			return fmt.Errorf("Lark helper did not exit cleanly: %w", child.waitErr)
		}
		return nil
	}
	err := inner.Close()
	cancel()
	finishStateLease(lease, func() {})
	return err
}

func (b *larkStateBackend) Catalog(ctx context.Context) (cat Catalog, err error) {
	err = b.operation(ctx, larkOperationPlan{shared: true}, nil, func(ctx context.Context, inner backend) error {
		var err error
		cat, err = inner.Catalog(ctx)
		if err == nil {
			b.mu.Lock()
			b.catalog = cat
			b.mu.Unlock()
		}
		return err
	})
	return cat, err
}

func (b *larkStateBackend) Call(ctx context.Context, call extension.Call, report func(extension.Progress)) (result extension.Result, err error) {
	err = b.operation(ctx, larkCallPlan(call), report, func(ctx context.Context, inner backend) error {
		cat, err := inner.Catalog(ctx)
		if err != nil {
			return &larkNotDispatchedError{cause: err}
		}
		matched := false
		b.mu.RLock()
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
		b.mu.RUnlock()
		if !matched {
			return &larkNotDispatchedError{cause: fmt.Errorf("tool %q changed; refresh the catalog before retrying", call.Tool)}
		}
		result, err = inner.Call(ctx, call, report) // exactly once; never replay
		return err
	})
	return result, err
}

func (b *larkStateBackend) GetPrompt(ctx context.Context, name string, args map[string]string) (text string, err error) {
	err = b.operation(ctx, larkOperationPlan{}, nil, func(ctx context.Context, inner backend) error {
		var err error
		text, err = inner.GetPrompt(ctx, name, args)
		return err
	})
	return text, err
}

func (b *larkStateBackend) ReadResource(ctx context.Context, uri string) (text string, err error) {
	err = b.operation(ctx, larkOperationPlan{}, nil, func(ctx context.Context, inner backend) error {
		var err error
		text, err = inner.ReadResource(ctx, uri)
		return err
	})
	return text, err
}

func (b *larkStateBackend) Close() error { b.cancel(); return nil }
