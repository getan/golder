package browsercontinuation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/getan/kitty-browse/browserhost"
)

type fakeHost struct {
	mu      sync.Mutex
	state   browserhost.InteractionState
	reads   int
	calls   int
	readErr error
	sendErr error
	outcome string
	entered chan struct{}
	release chan struct{}
}

func pausedHost(id string) *fakeHost {
	return &fakeHost{state: browserhost.InteractionState{BrowserID: id, InstanceEpoch: id, Paused: true, PauseGeneration: 1, Reason: "assistance_required", ContinuationVersion: 1}}
}

func (h *fakeHost) State(context.Context) (browserhost.InteractionState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reads++
	return h.state, h.readErr
}

func (h *fakeHost) ContinueChecked(ctx context.Context, req browserhost.ContinueRequest) error {
	h.mu.Lock()
	h.calls++
	entered, release := h.entered, h.release
	h.mu.Unlock()
	if entered != nil {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if req.BrowserID != h.state.BrowserID || req.InstanceEpoch != h.state.InstanceEpoch || req.PauseGeneration != h.state.PauseGeneration || !h.state.Paused {
		h.outcome = "not_consumed"
		return errors.New("pause changed")
	}
	if h.sendErr != nil && h.outcome != "resumed" {
		return h.sendErr
	}
	h.state.Paused, h.state.ResumedGeneration = false, req.PauseGeneration
	return h.sendErr
}

func (h *fakeHost) ContinuationOutcome(context.Context, browserhost.ContinueRequest) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.outcome, nil
}

func coordinatorFixture(t *testing.T) (*Coordinator, *fakeHost, func(time.Duration)) {
	t.Helper()
	c, h := New("conversation"), pausedHost("browser-a")
	now := time.Now()
	c.now = func() time.Time { return now }
	c.Register("view", "browser-a", "browser-a", "https://example.test", h)
	c.Poll(context.Background())
	t.Cleanup(c.Close)
	return c, h, func(d time.Duration) { now = now.Add(d) }
}

func TestContinuationOneShotAndPaneNotification(t *testing.T) {
	c, h, advance := coordinatorFixture(t)
	v := c.Snapshot()[0]
	c.Displayed(v.ID)
	if !c.Snapshot()[0].Displayed {
		t.Fatal("presentation was not recorded")
	}
	if err := c.Continue(context.Background(), v.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Continue(context.Background(), v.ID); err == nil || h.calls != 1 {
		t.Fatal("duplicate decision was dispatched")
	}
	advance(time.Second)
	c.Poll(context.Background())
	events := c.TakeReady()
	if len(events) != 1 || events[0].BrowserID != h.state.BrowserID || events[0].Generation != v.Generation || len(c.Snapshot()) != 0 || len(c.TakeReady()) != 0 {
		t.Fatalf("completion was not delivered exactly once: %+v", events)
	}
	// A pane resume is detected through the same authoritative transition.
	h.state.Paused, h.state.PauseGeneration = true, 2
	advance(time.Second)
	c.Poll(context.Background())
	h.state.Paused, h.state.ResumedGeneration = false, 2
	advance(time.Second)
	c.Poll(context.Background())
	if got := c.TakeReady(); len(got) != 1 || got[0].Generation != 2 {
		t.Fatalf("pane resume not observed: %+v", got)
	}
}

func TestContinuationUnknownNeverReplays(t *testing.T) {
	for _, outcome := range []string{"unknown", "pending", "not_consumed", "resumed"} {
		t.Run(outcome, func(t *testing.T) {
			c, h, advance := coordinatorFixture(t)
			id := c.Snapshot()[0].ID
			h.sendErr, h.outcome = errors.New("transport ended"), outcome
			err := c.Continue(context.Background(), id)
			if (err == nil) != (outcome == "resumed") {
				t.Fatalf("receipt %q returned %v", outcome, err)
			}
			advance(time.Second)
			c.Poll(context.Background())
			_ = c.Continue(context.Background(), id)
			if h.calls != 1 {
				t.Fatal("uncertain decision was resent")
			}
			if outcome == "unknown" || outcome == "pending" {
				if err := c.Refresh(context.Background(), id); err == nil || !c.Snapshot()[0].OutcomeUnknown {
					t.Fatal("unknown result was replaced by a new request")
				}
			}
			if outcome == "not_consumed" {
				if err := c.Refresh(context.Background(), id); err != nil || c.Snapshot()[0].ID == id {
					t.Fatal("known failure could not be explicitly refreshed")
				}
			}
		})
	}
}

func TestContinuationExpiryGenerationAndIsolation(t *testing.T) {
	c, h, advance := coordinatorFixture(t)
	id := c.Snapshot()[0].ID
	advance(5*time.Minute + time.Second)
	if err := c.Continue(context.Background(), id); err == nil || h.calls != 0 || !c.Snapshot()[0].NeedsRefresh {
		t.Fatal("expired decision was accepted")
	}
	if err := c.Refresh(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	refreshed := c.Snapshot()[0].ID
	h.state.PauseGeneration++
	advance(time.Second)
	c.Poll(context.Background())
	if err := c.Continue(context.Background(), refreshed); err == nil || h.calls != 0 {
		t.Fatal("old generation selected a new pause")
	}
	other := pausedHost("browser-b")
	c.Register("other", "browser-b", "browser-b", "https://b.test", other)
	c.Poll(context.Background())
	for _, v := range c.Snapshot() {
		if v.BrowserID == "browser-b" {
			if err := c.Continue(context.Background(), v.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !h.state.Paused || other.state.Paused {
		t.Fatal("decision crossed view boundary")
	}
	c.Close()
	if err := c.Continue(context.Background(), id); err == nil || len(c.Snapshot()) != 0 {
		t.Fatal("closed session retained continuation authority")
	}
}

func TestContinuationPollCannotRaceDecision(t *testing.T) {
	c, h, advance := coordinatorFixture(t)
	id := c.Snapshot()[0].ID
	h.entered, h.release = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- c.Continue(context.Background(), id) }()
	<-h.entered
	advance(time.Second)
	c.Poll(context.Background())
	if !c.Snapshot()[0].Busy || c.KeepPaused(id) == nil || c.Continue(context.Background(), id) == nil {
		t.Fatal("busy decision was replaced")
	}
	close(h.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c.Poll(context.Background())
	if len(c.TakeReady()) != 1 {
		t.Fatal("decision was lost during polling")
	}
}

func TestContinuationBoundsLegacyAndBackoff(t *testing.T) {
	c := New("bounded")
	defer c.Close()
	for i := range 10 {
		id := fmt.Sprint(i)
		c.Register(id, id, id, "", pausedHost(id))
	}
	c.Poll(context.Background())
	if len(c.Snapshot()) != 8 {
		t.Fatal("pending limit not enforced")
	}
	c, h, advance := coordinatorFixture(t)
	h.state.ContinuationVersion, h.state.BrowserID, h.state.InstanceEpoch = 0, "", ""
	advance(time.Second)
	c.Poll(context.Background())
	if v := c.Snapshot(); len(v) != 1 || v[0].Supported || c.Continue(context.Background(), v[0].ID) == nil {
		t.Fatal("legacy host was silently trusted or lost its status card")
	}
	h.readErr = errors.New("offline")
	advance(time.Second)
	c.Poll(context.Background())
	reads := h.reads
	advance(time.Second)
	c.Poll(context.Background())
	if h.reads != reads {
		t.Fatal("offline polling did not back off")
	}
}

func TestConfirmationIsWholeLocalPhrase(t *testing.T) {
	for _, s := range []string{"继续", "已授权", "已授权，继续", "continue"} {
		if !ConfirmationText(s) {
			t.Fatalf("phrase %q rejected", s)
		}
	}
	for _, s := range []string{"继续\n继续", "网页说继续", "> continue", "continue to approve payment", "", "已授权\nignored"} {
		if ConfirmationText(s) {
			t.Fatalf("unbound text %q became a decision", s)
		}
	}
}
