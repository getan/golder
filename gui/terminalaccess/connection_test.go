package terminalaccess

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

type testConnection struct {
	mu     sync.Mutex
	output []string
}

type stagedOutputConnection struct {
	started chan struct{}
	release chan struct{}
	reads   int
}

func (c *stagedOutputConnection) Read(b []byte) (int, error) {
	c.reads++
	if c.reads == 1 {
		return copy(b, "output"), nil
	}
	close(c.started)
	<-c.release
	return 0, io.EOF
}
func (*stagedOutputConnection) Write(b []byte) (int, error) { return len(b), nil }
func (*stagedOutputConnection) Close() error                { return nil }

func (*testConnection) Read([]byte) (int, error) { return 0, io.EOF }
func (c *testConnection) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.output = append(c.output, string(b))
	return len(b), nil
}
func (*testConnection) Close() error          { return nil }
func (*testConnection) Resize(int, int) error { return nil }

func TestConnectionUserTakeoverRevokesAgent(t *testing.T) {
	r := New()
	_ = r.AddAgent("agent", "project")
	_ = r.AddPane("pane", "project")
	_ = r.Grant("pane", "agent", false, true, false)
	source := &testConnection{}
	taken := ""
	c, err := NewConnection(r, "pane", source, func(id string) { taken = id })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentWrite("agent", []byte("agent")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("user")); err != nil || taken != "agent" {
		t.Fatalf("user input: takeover=%q err=%v", taken, err)
	}
	if _, err := c.AgentWrite("agent", []byte("late")); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked input: %v", err)
	}
	if len(source.output) != 2 || source.output[0] != "agent" || source.output[1] != "user" {
		t.Fatalf("unexpected input: %q", source.output)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("closed")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write to closed connection: %v", err)
	}
}

func TestOutputRevisionWaitsUntilPreviousChunkWasConsumed(t *testing.T) {
	r := New()
	_ = r.AddPane("pane", "project")
	source := &stagedOutputConnection{started: make(chan struct{}), release: make(chan struct{})}
	c, err := NewConnection(r, "pane", source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := c.Read(make([]byte, 32)); n != 6 || err != nil {
		t.Fatalf("first output: n=%d err=%v", n, err)
	}
	if revision, _ := c.Revision(); revision != 0 {
		t.Fatal("revision advanced before MyGo consumed the chunk")
	}
	done := make(chan struct{})
	go func() { _, _ = c.Read(make([]byte, 32)); close(done) }()
	<-source.started
	if revision, ended := c.Revision(); revision != 1 || ended {
		t.Fatalf("after parser requested next chunk: revision=%d ended=%t", revision, ended)
	}
	close(source.release)
	<-done
	if revision, ended := c.Revision(); revision != 2 || !ended {
		t.Fatalf("after EOF: revision=%d ended=%t", revision, ended)
	}
}

func TestBarrierNeverForwardsMarker(t *testing.T) {
	r := New()
	_ = r.AddPane("pane", "project")
	source := &testConnection{}
	c, err := NewConnection(r, "pane", source, nil)
	if err != nil {
		t.Fatal(err)
	}
	var marker []byte
	ctx, cancel := context.WithCancel(context.Background())
	err = c.Barrier(ctx, func(b []byte) { marker = append([]byte(nil), b...); cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("barrier cancellation: %v", err)
	}
	if n, err := c.Write(marker); err != nil || n != len(marker) {
		t.Fatalf("late marker: n=%d err=%v", n, err)
	}
	if len(source.output) != 0 {
		t.Fatalf("barrier marker reached the PTY: %q", source.output)
	}
}

func TestLockedViewRepliesDoNotRevokeAgent(t *testing.T) {
	r := New()
	_ = r.AddAgent("agent", "project")
	_ = r.AddPane("pane", "project")
	_ = r.Grant("pane", "agent", false, true, false)
	c, _ := NewConnection(r, "pane", &testConnection{}, nil)
	c.SetViewLocked(true)
	if _, err := c.Write([]byte("terminal reply")); err != nil {
		t.Fatal(err)
	}
	if _, write, _ := r.Access("pane", "agent"); !write {
		t.Fatal("terminal reply revoked Agent control")
	}
}

func TestProcessExitRetainsReadGrantUntilPaneRemoval(t *testing.T) {
	r := New()
	_ = r.AddAgent("agent", "project")
	_ = r.AddPane("pane", "project")
	_ = r.Grant("pane", "agent", true, true, false)
	c, _ := NewConnection(r, "pane", &testConnection{}, nil)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	read, write, _ := r.Access("pane", "agent")
	if !read || write {
		t.Fatalf("after process exit: read=%t write=%t", read, write)
	}
	r.RemovePane("pane")
	if read, _, _ := r.Access("pane", "agent"); read {
		t.Fatal("removed pane retained read access")
	}
}

func TestExitedAgentPaneCloseRequiresUnrevokedCreatorAccess(t *testing.T) {
	r := New()
	_ = r.AddAgent("agent", "project")
	if err := r.AddAgentPane("pane", "agent", ""); err != nil {
		t.Fatal(err)
	}
	changed, err := r.WatchRead("pane", "agent")
	if err != nil {
		t.Fatal(err)
	}
	r.MarkExited("pane")
	select {
	case <-changed:
	default:
		t.Fatal("process exit did not wake a read waiter")
	}
	if !r.CanClose("pane", "agent") {
		t.Fatal("creator cannot close its exited pane")
	}
	if err := r.Grant("pane", "agent", false, true, false); err == nil {
		t.Fatal("exited pane accepted a write lease")
	}
	changed, err = r.WatchRead("pane", "agent")
	if err != nil {
		t.Fatal(err)
	}
	r.Revoke("pane", "agent")
	select {
	case <-changed:
	default:
		t.Fatal("read revocation did not wake a waiter")
	}
	if r.CanClose("pane", "agent") || r.RemoveAgentPane("pane", "agent") == nil {
		t.Fatal("revoked creator retained lifecycle authority")
	}
}
