package workspace

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/getan/golder/internal/cli/workspacecmd"
	"github.com/getan/golder/internal/privatepath"
)

// lockSession reserves a name independently of its socket and state overrides.
// The daemon holds this lease through final cleanup. Its inode is outside the
// removable session directory, and must never be unlinked.
func lockSession(opts workspacecmd.Options) (*stateLease, error) {
	if err := workspacecmd.CheckIdentity(opts); err != nil {
		return nil, err
	}
	for _, dir := range []string{opts.Home, filepath.Join(opts.Home, "workspace"), filepath.Dir(workspacecmd.SessionLockPath(opts))} {
		if err := privatepath.EnsureDir(dir); err != nil {
			return nil, err
		}
	}
	return lockPath(workspacecmd.SessionLockPath(opts))
}

// reserveStart either obtains the name's lifetime lease or returns the verified
// owner already using it. A competing default run waits for readiness rather
// than starting a second daemon; new never attaches to the winner.
func reserveStart(ctx context.Context, opts workspacecmd.Options) (workspacecmd.Options, *stateLease, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		lease, err := lockSession(opts)
		if err == nil {
			fresh, err := workspacecmd.ReloadSession(opts)
			if err != nil {
				lease.Close()
				return opts, nil, false, err
			}
			if fresh.Command == workspacecmd.CommandNew && fresh.Exists {
				lease.Close()
				return fresh, nil, false, nameExists(fresh.Session)
			}
			return fresh, lease, false, nil
		}
		if !errors.Is(err, errLeaseBusy) {
			return opts, nil, false, err
		}
		if opts.Command == workspacecmd.CommandNew {
			return opts, nil, false, nameExists(opts.Session)
		}
		fresh, err := workspacecmd.ReloadSession(opts)
		if err != nil {
			return opts, nil, false, err
		}
		// A name lock precedes its record/owner publication. Missing endpoints
		// in that small interval are not evidence of a stopped session.
		record, readErr := readOwner(fresh)
		if readErr == nil && fresh.Exists {
			if _, err := callOwner(ctx, record, "status"); err == nil {
				if err := rejectStartOverrides(fresh); err != nil {
					return fresh, nil, false, err
				}
				return fresh, nil, true, nil
			} else if !refusedSocket(err) {
				return fresh, nil, false, err
			}
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return fresh, nil, false, readErr
		}
		select {
		case <-ctx.Done():
			return opts, nil, false, fmt.Errorf("workspace: session %q is busy or still starting: %w", opts.Session, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func nameExists(name string) error {
	return fmt.Errorf("workspace: session %q already exists; attach to it, run it if stopped, or choose another name", name)
}

// requireIdle is read-only: status/stop must not create a name lock or chmod
// it. A held lease without a ready owner means starting/stopping, not stopped.
func requireIdle(opts workspacecmd.Options) error {
	if opts.ManifestPath == "" {
		return nil
	}
	lease, err := openLease(workspacecmd.SessionLockPath(opts), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: session %q is busy or its lease cannot be verified: %w", opts.Session, err)
	}
	lease.Close()
	return nil
}

func rejectStartOverrides(opts workspacecmd.Options) error {
	if opts.CwdExplicit || opts.Shell || len(opts.AgentArgs) != 0 {
		return fmt.Errorf("workspace: session %q is already running; -C/--cwd, --shell and agent arguments apply only when starting; use 'golder workspace attach %s'", opts.Session, opts.Session)
	}
	return nil
}

// probeOwner never mistakes a timeout or an invalid proof for a stopped
// daemon. A stale owned record is stopped only when both endpoints are
// unequivocally absent/refused. It does not delete anything.
func probeOwner(ctx context.Context, opts workspacecmd.Options) (ownerRecord, ownerReply, bool, error) {
	if opts.DiscoveryError != "" {
		return ownerRecord{}, ownerReply{}, false, errors.New(opts.DiscoveryError)
	}
	if opts.ManifestPath != "" && !opts.Exists && opts.Socket == "" {
		return ownerRecord{}, ownerReply{}, false, requireIdle(opts)
	}
	r, err := readOwner(opts)
	if err == nil {
		reply, callErr := callOwner(ctx, r, "status")
		if callErr == nil {
			return r, reply, true, nil
		}
		if !refusedSocket(callErr) {
			return r, ownerReply{}, false, fmt.Errorf("workspace: owner endpoint is not reachable: %w", callErr)
		}
		if err := deadSocket(ctx, r.Socket); err != nil {
			return r, ownerReply{}, false, err
		}
		return r, ownerReply{}, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return r, ownerReply{}, false, err
	}
	// An unrecorded socket, even a dead one, is not ours to adopt or remove.
	if _, err := os.Lstat(opts.Socket); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return r, ownerReply{}, false, err
		}
		return r, ownerReply{}, false, errors.New("workspace: a control socket exists without this workspace's ownership record; refusing to adopt an unrelated daemon")
	}
	return r, ownerReply{}, false, nil
}

func deadSocket(ctx context.Context, path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("workspace: %q is not an owned socket", path)
	}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("workspace: %q is still listening without a reachable owner", path)
	}
	if !refusedSocket(err) {
		return fmt.Errorf("workspace: cannot prove %q is stopped: %w", path, err)
	}
	return nil
}

// Remove only touches the fixed named-session directory, after both its name
// and state leases are held and its owner has been proved stopped. Overrides
// outside that directory (a custom state/config/socket) are never deleted.
func (b *Backend) Remove(ctx context.Context, opts workspacecmd.Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := supported(); err != nil {
		return err
	}
	if err := workspacecmd.CheckIdentity(opts); err != nil {
		return err
	}
	if _, err := os.Lstat(opts.SessionDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	lease, err := lockSession(opts)
	if err != nil {
		return fmt.Errorf("workspace: cannot remove session %q while it is running or busy: %w", opts.Session, err)
	}
	defer lease.Close()
	opts, err = workspacecmd.ReloadSession(opts)
	if err != nil {
		return err
	}
	if !opts.Exists {
		return fmt.Errorf("workspace: no session record for %q; refusing to remove unrecognised files", opts.Session)
	}
	_, _, running, err := probeOwner(ctx, opts)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("workspace: session %q is running; stop it before removing it", opts.Session)
	}
	if _, err := os.Lstat(opts.StateDir); err == nil {
		st, err := lockState(opts.StateDir)
		if err != nil {
			return err
		}
		defer st.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.RemoveAll(opts.SessionDir)
}
