//go:build unix

package extensions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/golder/extension"
)

func TestLarkConcurrencyHostFixture(t *testing.T) {
	if os.Getenv("LARK_CONCURRENT_HOST_FIXTURE") != "1" {
		return
	}
	parent, id := os.Getenv("LARK_CONCURRENT_PARENT"), os.Getenv("LARK_CONCURRENT_ID")
	_ = os.WriteFile(filepath.Join(parent, id+".ready"), []byte("ready"), 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := acquireLarkLease(ctx, filepath.Join(parent, "home"), larkOperationPlan{shared: true}, func(extension.Progress) {
		_ = os.WriteFile(filepath.Join(parent, id+".queued"), []byte("queued"), 0o600)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer release()
	_ = os.WriteFile(filepath.Join(parent, id+".held"), []byte("held"), 0o600)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(parent, "release")); err == nil {
			release()
			os.Exit(0)
		}
		select {
		case <-ctx.Done():
			os.Exit(2)
		case <-ticker.C:
		}
	}
}

func TestLarkConcurrencyBoundCoordinatesIndependentHosts(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var held []func()
	defer func() {
		for _, release := range held {
			release()
		}
	}()
	for range larkParallelLimit - 1 {
		release, err := acquireLarkLease(ctx, filepath.Join(parent, "home"), larkOperationPlan{shared: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}
	exe, _ := os.Executable()
	done := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestLarkConcurrencyHostFixture$")
		cmd.Env = append(os.Environ(), "LARK_CONCURRENT_HOST_FIXTURE=1", "LARK_CONCURRENT_PARENT="+parent, "LARK_CONCURRENT_ID="+id)
		go func() {
			output, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("%w: %s", err, output)
			}
			done <- err
		}()
		waitLarkStateFile(t, ctx, filepath.Join(parent, id+".ready"))
		if id == "first" {
			waitLarkStateFile(t, ctx, filepath.Join(parent, id+".held"))
		} else {
			waitLarkStateFile(t, ctx, filepath.Join(parent, id+".queued"))
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "second.held")); !os.IsNotExist(err) {
		t.Fatalf("fifth operation entered across process boundary: %v", err)
	}
	held[0]()
	waitLarkStateFile(t, ctx, filepath.Join(parent, "second.held"))
	if err := os.WriteFile(filepath.Join(parent, "release"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestLarkSourceOwnershipWaitIsCancellable(t *testing.T) {
	s := &source{}
	s.enabled.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := lockLarkSource(ctx, s); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("authorization ownership wait ignored cancellation: %v", err)
	}
}
