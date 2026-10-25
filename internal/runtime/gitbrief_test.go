package runtime

import (
	"context"
	"os"
	"path/filepath"
	stdRuntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestGitBriefOutputLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		max  int
	}{
		{"lines", strings.Repeat(" M file\n", 100_000), 40 * len(" M file\n")},
		{"bytes", strings.Repeat("x", 100_000), gitBriefMaxBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := &gitBriefOutput{cancel: cancel}
			n, err := out.Write([]byte(tc.text))
			if err != nil || n != len(tc.text) || !out.truncated || out.buf.Len() != tc.max || ctx.Err() == nil {
				t.Fatalf("n=%d err=%v truncated=%v bytes=%d cancelled=%v", n, err, out.truncated, out.buf.Len(), ctx.Err())
			}
			_, _ = out.Write([]byte("discarded"))
			if out.buf.Len() != tc.max {
				t.Fatal("writes after the cap must not grow the buffer")
			}
		})
	}
}

func fakeBriefGit(t *testing.T, script string) string {
	t.Helper()
	if stdRuntime.GOOS == "windows" {
		t.Skip("POSIX helper; bounded writer tests run on every platform")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestGitBriefTimeout(t *testing.T) {
	dir := fakeBriefGit(t, "exec sleep 10\n")
	start := time.Now()
	got := gitStatusBriefWithTimeout(context.Background(), dir, 50*time.Millisecond)
	if got != "" || time.Since(start) > time.Second {
		t.Fatalf("slow git was not skipped promptly: %q, elapsed=%s", got, time.Since(start))
	}
}

func TestGitBriefParentCancellation(t *testing.T) {
	dir := fakeBriefGit(t, ": > started\nexec sleep 10\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan string, 1)
	go func() { done <- gitStatusBriefWithTimeout(ctx, dir, 10*time.Second) }()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("git helper did not start")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("cancelled git brief = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not stop the git probe")
	}
}

func TestGitBriefSuccessFailureAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   string
	}{
		{"success", "printf '## main\\n M file\\n'\n", "## main\n M file"},
		{"not a repo", "exit 128\n", ""},
		{"truncated", "i=0; while [ $i -lt 100 ]; do printf ' M file\\n'; i=$((i+1)); done\n", strings.TrimRight(strings.Repeat(" M file\n", 40), "\n") + "\n… (truncated)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakeBriefGit(t, tc.script)
			if got := gitStatusBrief(context.Background(), dir); got != tc.want {
				t.Fatalf("git brief = %q, want %q", got, tc.want)
			}
		})
	}
}
