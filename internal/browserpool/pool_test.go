package browserpool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/kitty-browse/browserhost"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperFlag {
		if RunHelper(context.Background(), os.Stdin) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestProfileOwnerIsSharedAcrossFrontends(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	profile := filepath.Join(t.TempDir(), "default.profile")
	a, err := Options(profile, "project/default", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Options(profile, "project/default", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.RuntimeDir != b.RuntimeDir || a.StateDir != b.StateDir {
		t.Fatal("same profile resolved to different brokers")
	}
	if browserhost.ViewSocket(a.RuntimeDir, browserhost.ViewID("a", "browser")) == browserhost.ViewSocket(b.RuntimeDir, browserhost.ViewID("b", "browser")) {
		t.Fatal("different conversations shared a socket")
	}
	if len(browserhost.ViewSocket(a.RuntimeDir, browserhost.ViewID("a", "browser"))) > 103 {
		t.Fatal("socket exceeds Unix limit")
	}
}

func TestSharedHelperStartsAndExitsIdleWithoutChromium(t *testing.T) {
	dir, err := os.MkdirTemp("", "gb-pool-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	opts := browserhost.SharedOptions{StateDir: filepath.Join(real, "state"), RuntimeDir: filepath.Join(real, "run"), Browser: browserhost.Options{ProfileDir: filepath.Join(real, "profile")}, IdleTimeout: 100 * time.Millisecond}
	if len(browserhost.SharedSocket(opts.RuntimeDir)) > 103 {
		t.Skip("temporary root is too long for Unix sockets")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := start(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if !browserhost.SharedReady(ctx, opts.RuntimeDir) {
		t.Fatal("helper did not serve readiness")
	}
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Lstat(browserhost.SharedSocket(opts.RuntimeDir)); os.IsNotExist(err) {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("idle helper did not remove its socket")
		case <-tick.C:
		}
	}
}
