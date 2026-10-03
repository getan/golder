package execsess

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
}

func bashArgv(command string) []string {
	return []string{"bash", "-c", command}
}

func TestHeadTailBufferReadFrom(t *testing.T) {
	b := &headTailBuffer{headMax: 4, tailMax: 4}
	// 15 bytes: head keeps "AAAB", the tail window keeps the last 4 ("DEEE"),
	// and the middle 7 bytes ("BBCCCDD") are dropped.
	b.Write([]byte("AAABBBCCCDDDEEE"))

	head, tail, omitted, next := b.readFrom(0)
	if string(head) != "AAAB" || string(tail) != "DEEE" {
		t.Fatalf("head=%q tail=%q, want AAAB / DEEE", head, tail)
	}
	if omitted != 7 {
		t.Fatalf("omitted = %d, want 7", omitted)
	}
	if next != 15 {
		t.Fatalf("next = %d, want 15", next)
	}

	// Resuming from the middle reports only the dropped remainder plus tail.
	head, tail, omitted, _ = b.readFrom(6)
	if head != nil || string(tail) != "DEEE" || omitted != 5 {
		t.Fatalf("readFrom(6) = %q/%q/%d, want nil/DEEE/5", head, tail, omitted)
	}

	// A fully-consumed reader sees nothing.
	if h, tl, om, _ := b.readFrom(15); h != nil || tl != nil || om != 0 {
		t.Fatalf("readFrom(15) = %q/%q/%d, want empty", h, tl, om)
	}
}

func TestHeadTailBufferSmallWritesStayExact(t *testing.T) {
	b := newHeadTailBuffer(64)
	b.Write([]byte("hello"))
	b.Write([]byte(" world"))
	head, tail, omitted, _ := b.readFrom(0)
	if got := string(head) + string(tail); got != "hello world" || omitted != 0 {
		t.Fatalf("got %q omitted=%d, want full text", got, omitted)
	}
}

func TestSessionRunToExit(t *testing.T) {
	requireShell(t)
	m := NewManager()
	s, err := m.Start(StartRequest{Command: "echo hi", Argv: bashArgv("echo hi")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !s.Wait(context.Background(), 5*time.Second, WaitExit) {
		t.Fatalf("session did not exit")
	}
	if got := s.ReadNew(); !strings.Contains(got, "hi") {
		t.Fatalf("ReadNew = %q, want hi", got)
	}
	if got := s.ReadNew(); got != "" {
		t.Fatalf("second ReadNew = %q, want empty", got)
	}
	snap := s.Snapshot()
	if snap.Status != StatusExited || snap.ExitCode != 0 || snap.TimedOut {
		t.Fatalf("snapshot = %+v, want exited 0", snap)
	}
}

func TestSessionExitCode(t *testing.T) {
	requireShell(t)
	m := NewManager()
	s, err := m.Start(StartRequest{Command: "exit 3", Argv: bashArgv("exit 3")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Wait(context.Background(), 5*time.Second, WaitExit)
	if snap := s.Snapshot(); snap.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", snap.ExitCode)
	}
}

func TestSessionObserver(t *testing.T) {
	requireShell(t)
	m := NewManager()
	var mu sync.Mutex
	var chunks []string
	s, err := m.Start(StartRequest{
		Command: "printf a; printf b",
		Argv:    bashArgv("printf a; printf b"),
		Observer: func(chunk string) {
			mu.Lock()
			chunks = append(chunks, chunk)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Wait(context.Background(), 5*time.Second, WaitExit)
	mu.Lock()
	joined := strings.Join(chunks, "")
	mu.Unlock()
	if !strings.Contains(joined, "ab") {
		t.Fatalf("observed chunks = %q, want to contain ab", joined)
	}
}

func TestSessionKill(t *testing.T) {
	requireShell(t)
	m := NewManager()
	s, err := m.Start(StartRequest{Command: "sleep 30", Argv: bashArgv("sleep 30")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	s.Kill()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Kill took %s, want a prompt process-group kill", elapsed)
	}
	if !s.Exited() {
		t.Fatal("session still running after Kill")
	}
}

func TestSessionInterrupt(t *testing.T) {
	requireShell(t)
	m := NewManager()
	s, err := m.Start(StartRequest{Command: "sleep 30", Argv: bashArgv("sleep 30")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	s.Interrupt()
	if !s.Wait(context.Background(), 3*time.Second, WaitExit) {
		t.Fatal("session did not exit after interrupt")
	}
}

// A process that ignores SIGINT must not be able to pin a session: the second
// interrupt escalates to Kill (SIGTERM, then SIGKILL).
func TestSessionInterruptEscalates(t *testing.T) {
	requireShell(t)
	m := NewManager()
	s, err := m.Start(StartRequest{
		Command: "trap '' INT TERM; while :; do sleep 1; done",
		Argv:    bashArgv("trap '' INT TERM; while :; do sleep 1; done"),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	s.Interrupt()
	time.Sleep(300 * time.Millisecond)
	if s.Exited() {
		t.Fatal("session exited on the first interrupt despite trapping SIGINT")
	}
	s.Interrupt()
	if !s.Wait(context.Background(), 3*time.Second, WaitExit) {
		t.Fatal("session survived the second interrupt (kill escalation failed)")
	}
}

func TestSessionTimeout(t *testing.T) {
	requireShell(t)
	m := NewManager()
	s, err := m.Start(StartRequest{
		Command: "sleep 30",
		Argv:    bashArgv("sleep 30"),
		Timeout: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !s.Wait(context.Background(), 3*time.Second, WaitExit) {
		t.Fatal("session did not exit at its deadline")
	}
	snap := s.Snapshot()
	if !snap.TimedOut || snap.Status != StatusExited {
		t.Fatalf("snapshot = %+v, want timed out + exited", snap)
	}
	if snap.Timeout != 150*time.Millisecond {
		t.Fatalf("timeout = %s, want 150ms", snap.Timeout)
	}
}

func TestManagerPrunesExitedAtCapacity(t *testing.T) {
	requireShell(t)
	old := maxSessions
	maxSessions = 2
	defer func() { maxSessions = old }()

	m := NewManager()
	s1, err := m.Start(StartRequest{Command: "true", Argv: []string{"true"}})
	if err != nil {
		t.Fatalf("Start s1: %v", err)
	}
	if !s1.Wait(context.Background(), 3*time.Second, WaitExit) {
		t.Fatal("s1 did not exit")
	}
	if _, err := m.Start(StartRequest{Command: "sleep 30", Argv: bashArgv("sleep 30")}); err != nil {
		t.Fatalf("Start s2: %v", err)
	}
	// Full, but s1 is exited: the manager must prune it rather than fail.
	if _, err := m.Start(StartRequest{Command: "echo ok", Argv: bashArgv("echo ok")}); err != nil {
		t.Fatalf("Start s3 = %v, want prune of the exited session", err)
	}
	m.KillAll()
}

// At capacity with every session still running, the manager evicts the least
// recently used live session instead of rejecting the new command (codex's
// LRU fallback in its unified-exec store).
func TestManagerEvictsLRUWhenAllRunning(t *testing.T) {
	requireShell(t)
	old := maxSessions
	maxSessions = 2
	defer func() { maxSessions = old }()

	m := NewManager()
	s1, err := m.Start(StartRequest{Command: "sleep 30", Argv: bashArgv("sleep 30")})
	if err != nil {
		t.Fatalf("Start s1: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := m.Start(StartRequest{Command: "sleep 30 | cat", Argv: bashArgv("sleep 30 | cat")}); err != nil {
		t.Fatalf("Start s2: %v", err)
	}
	// Full, all running: s3 must evict the least recently used (s1) and start.
	if _, err := m.Start(StartRequest{Command: "echo ok", Argv: bashArgv("echo ok")}); err != nil {
		t.Fatalf("Start s3 = %v, want LRU eviction rather than a capacity failure", err)
	}
	if !s1.Wait(context.Background(), 3*time.Second, WaitExit) {
		t.Fatal("evicted session s1 still running, want it terminated")
	}
	m.KillAll()
}

// The most recently used sessions are protected: a freshly polled session is
// not the one evicted when a newer command arrives.
func TestManagerProtectsRecentlyUsed(t *testing.T) {
	requireShell(t)
	old := maxSessions
	maxSessions = 2
	defer func() { maxSessions = old }()

	m := NewManager()
	s1, err := m.Start(StartRequest{Command: "sleep 30", Argv: bashArgv("sleep 30")})
	if err != nil {
		t.Fatalf("Start s1: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	s2, err := m.Start(StartRequest{Command: "sleep 30 | cat", Argv: bashArgv("sleep 30 | cat")})
	if err != nil {
		t.Fatalf("Start s2: %v", err)
	}
	// Poll s1 so it becomes the most recently used session, then push a third
	// command: s2 (untouched since its spawn) is now the LRU candidate.
	s1.Wait(context.Background(), 10*time.Millisecond, WaitExit)
	if _, err := m.Start(StartRequest{Command: "echo ok", Argv: bashArgv("echo ok")}); err != nil {
		t.Fatalf("Start s3 = %v, want eviction of the least recently used session", err)
	}
	if !s2.Wait(context.Background(), 3*time.Second, WaitExit) {
		t.Fatal("untouched session s2 still running, want the LRU pick evicted")
	}
	if s1.Exited() {
		t.Fatal("recently polled session s1 was evicted, want it protected")
	}
	m.KillAll()
}

func TestManagerCleanupRunsOnStartFailure(t *testing.T) {
	m := NewManager()
	called := false
	_, err := m.Start(StartRequest{
		Argv:    []string{"/nonexistent/pigo-test-binary"},
		Cleanup: func() { called = true },
	})
	if err == nil {
		t.Fatal("Start with a missing binary = nil error")
	}
	if !called {
		t.Fatal("Cleanup did not run after a failed spawn")
	}
}
