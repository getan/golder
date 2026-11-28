// Package browserpool assembles the trusted per-profile browser broker. Browser
// automation sees only conversation sockets; operator credentials stay here.
package browserpool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/getan/kitty-browse/browserhost"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/privatepath"
	"github.com/getan/golder/internal/proc"
)

const HelperFlag = "--golder-browser-hub"

// Prepare creates only the derived broker directories before a Linux browser
// helper enters its per-call filesystem envelope. Discovery stays read-only.
func Prepare(opts browserhost.SharedOptions) error {
	for _, dir := range []string{filepath.Dir(opts.StateDir), opts.StateDir, filepath.Dir(opts.RuntimeDir), opts.RuntimeDir} {
		if err := privatepath.EnsureDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// Options derives stable private paths from the profile, not a frontend PID.
func Options(profile, login, proxy string) (browserhost.SharedOptions, error) {
	profile, err := filepath.Abs(profile)
	if err != nil {
		return browserhost.SharedOptions{}, err
	}
	if golderhome.Dir() == "" {
		return browserhost.SharedOptions{}, errors.New("browser state root unavailable")
	}
	sum := sha256.Sum256([]byte(profile))
	key := hex.EncodeToString(sum[:8])
	state := golderhome.File(filepath.Join("browser-hubs", key))
	runtimeDir := browserRuntimeDir(key, os.Getenv)
	opts := browserhost.DefaultOptions()
	opts.URL, opts.Socket = "", ""
	opts.ProfileDir, opts.LoginProfile, opts.ProxyServer = profile, login, proxy
	opts.Chrome = os.Getenv("KITTY_BROWSE_CHROME")
	opts.RequireSandbox = true
	return browserhost.SharedOptions{Browser: opts, StateDir: state, RuntimeDir: runtimeDir}, nil
}

// Open starts the broker once when needed; concurrent helpers arbitrate using
// its process lock. Requests are never automatically replayed after dispatch.
func Open(ctx context.Context, opts browserhost.SharedOptions, conversation, instance, url string) (*browserhost.RemoteView, error) {
	if conversation == "" {
		return nil, errors.New("browser launch requires a conversation identity")
	}
	if !browserhost.SharedReady(ctx, opts.RuntimeDir) {
		if err := start(ctx, opts); err != nil {
			return nil, err
		}
	}
	return browserhost.OpenShared(ctx, opts, browserhost.ViewID(conversation, instance), url)
}

func start(ctx context.Context, opts browserhost.SharedOptions) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	cmd := proc.Command(exe, HelperFlag)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	home, err := os.MkdirTemp("", "golder-browser-home-")
	if err != nil {
		return err
	}
	cmd.Env = browserHelperEnv(home)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		os.RemoveAll(home)
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); _ = os.RemoveAll(home) }()
	deadline := time.NewTimer(browserHubReadyTimeout())
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var exitErr error
	for {
		if browserhost.SharedReady(ctx, opts.RuntimeDir) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("browser hub did not become ready (helper exit: %v); inspect before retrying", exitErr)
		case err := <-exited:
			// A losing concurrent helper exits after the winner acquired the
			// owner lock, possibly before its socket is listening. Only continue
			// readiness observation; no launch or page action is replayed.
			exitErr = err
			exited = nil
		case <-tick.C:
		}
	}
}

// RunHelper is called before normal CLI/GUI initialization. Configuration is
// read from the parent's pipe, not argv or any repository file.
func RunHelper(ctx context.Context, in io.Reader) error {
	var opts browserhost.SharedOptions
	d := json.NewDecoder(io.LimitReader(in, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&opts); err != nil {
		return err
	}
	return browserhost.ServeShared(ctx, opts)
}
