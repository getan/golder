// Package browsercontinuation owns local, person-originated browser decisions.
// Request IDs are presentation handles, never bearer capabilities. This package
// is used by trusted frontends and is not a model-facing tool.
package browsercontinuation

import (
	"context"
	"crypto/rand"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/getan/kitty-browse/browserhost"
)

type Host interface {
	State(context.Context) (browserhost.InteractionState, error)
	ContinueChecked(context.Context, browserhost.ContinueRequest) error
	ContinuationOutcome(context.Context, browserhost.ContinueRequest) (string, error)
}

var ErrRevoked = errors.New("browser continuation authority was revoked")

type View struct {
	ID, BrowserID, Origin, Reason, Error string
	InstanceEpoch                        string
	Generation                           uint64
	ExpiresAt                            time.Time
	Busy, Supported, KeptPaused          bool
	NeedsRefresh, OutcomeUnknown         bool
	Displayed                            bool
	RetryAfterMS                         int64
}

type entry struct {
	host                Host
	epoch, browser, key string
	state               browserhost.InteractionState
	view                View
	consumed            bool
	failures            int
	nextPoll            time.Time
	lastReady           Ready
}

type Ready struct {
	BrowserID  string
	Generation uint64
	RequestID  string
}

type Coordinator struct {
	mu       sync.Mutex
	pollMu   sync.Mutex
	session  string
	views    map[string]*entry
	ready    []Ready
	closed   bool
	now      func() time.Time
	lifetime context.Context
	cancel   context.CancelFunc
}

func New(session string) *Coordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{session: session, views: make(map[string]*entry), now: time.Now, lifetime: ctx, cancel: cancel}
}

func (c *Coordinator) Session() string { return c.session }

// Register is called only after the embedder has authenticated and attested a
// managed view. A new epoch revokes any outstanding decision for its old view.
func (c *Coordinator) Register(key, browser, epoch, origin string, h Host) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || key == "" || browser == "" || epoch == "" || h == nil {
		return
	}
	if old := c.views[key]; old != nil && old.browser == browser && old.epoch == epoch {
		return
	}
	if len(c.views) >= 8 && c.views[key] == nil {
		return
	}
	c.views[key] = &entry{host: h, key: key, browser: browser, epoch: epoch, view: View{BrowserID: browser, InstanceEpoch: epoch, Origin: origin}}
}

func (c *Coordinator) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.cancel()
	clear(c.views)
	c.ready = nil
}

func (c *Coordinator) Poll(ctx context.Context) {
	// Multiple frontends may observe one coordinator, but never subscribe twice
	// to the same view or race a snapshot against an in-flight decision.
	if !c.pollMu.TryLock() {
		return
	}
	defer c.pollMu.Unlock()
	c.mu.Lock()
	entries := make([]*entry, 0, len(c.views))
	for _, e := range c.views {
		if !e.view.Busy && !c.now().Before(e.nextPoll) {
			entries = append(entries, e)
		}
	}
	c.mu.Unlock()
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		op, cancel := context.WithTimeout(ctx, 2*time.Second)
		stop := context.AfterFunc(c.lifetime, cancel)
		s, err := e.host.State(op)
		stop()
		cancel()
		c.mu.Lock()
		if c.closed || c.views[e.key] != e || e.view.Busy {
			c.mu.Unlock()
			continue
		}
		if err != nil {
			if errors.Is(err, ErrRevoked) {
				delete(c.views, e.key)
				c.mu.Unlock()
				continue
			}
			e.failures++
			e.nextPoll = c.now().Add(time.Second * time.Duration(1<<min(e.failures, 3)))
			e.view.Error = "Browser connection unavailable; checking again with backoff"
			c.mu.Unlock()
			continue
		}
		if s.Closed || s.ContinuationVersion != 0 && (s.BrowserID != e.browser || s.InstanceEpoch != e.epoch) {
			delete(c.views, e.key)
			c.mu.Unlock()
			continue
		}
		if e.failures != 0 && !e.view.OutcomeUnknown && !e.view.NeedsRefresh {
			e.view.Error = ""
		}
		e.failures, e.nextPoll = 0, c.now().Add(time.Second)
		prior := e.state
		e.state = s
		e.view.Supported = s.ContinuationVersion == 1
		if s.Paused && (e.view.ID == "" || e.view.Generation != s.PauseGeneration) {
			e.view.ID, e.view.Generation, e.view.ExpiresAt = rand.Text(), s.PauseGeneration, c.now().Add(5*time.Minute)
			e.view.KeptPaused, e.view.NeedsRefresh, e.view.OutcomeUnknown, e.view.Displayed = false, false, false, false
			e.view.Error, e.consumed = "", false
			e.lastReady = Ready{}
		}
		e.view.Reason = s.Reason
		e.view.RetryAfterMS = s.RetryAfterMS
		if !s.Paused && prior.Paused && s.ResumedGeneration == prior.PauseGeneration {
			if !e.view.KeptPaused {
				e.lastReady = Ready{e.browser, prior.PauseGeneration, e.view.ID}
				c.ready = append(c.ready, e.lastReady)
				if len(c.ready) > 8 {
					c.ready = c.ready[len(c.ready)-8:]
				}
			}
			e.view.ID = ""
		}
		unknown := e.view.OutcomeUnknown
		req := request(e)
		c.mu.Unlock()
		if unknown {
			query, cancel := context.WithTimeout(ctx, time.Second)
			outcome, err := e.host.ContinuationOutcome(query, req)
			cancel()
			c.mu.Lock()
			if !c.closed && c.views[e.key] == e && e.view.ID == req.RequestID && err == nil {
				c.recordOutcome(e, outcome)
			}
			c.mu.Unlock()
		}
	}
}

func (c *Coordinator) Snapshot() []View {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []View
	for _, e := range c.views {
		if e.state.Paused {
			v := e.view
			if c.now().After(v.ExpiresAt) && !v.OutcomeUnknown && !e.consumed {
				v.Error, v.NeedsRefresh = "Confirmation expired; refresh this request", true
			}
			v.Busy = v.Busy || e.consumed
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *Coordinator) TakeReady() []Ready {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.ready
	c.ready = nil
	return out
}

// ReadyFor closes the race where a person resumes before the run's tool-end
// event is delivered. It is still exact-generation state, not a reusable grant;
// frontends consume their run intent atomically when starting a continuation.
func (c *Coordinator) ReadyFor(browser string, generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.views {
		if !c.closed && e.browser == browser && !e.state.Paused && e.lastReady.Generation == generation && e.lastReady.BrowserID == browser {
			return true
		}
	}
	return false
}

func (c *Coordinator) KeepPaused(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.views {
		if !c.closed && e.view.ID == id && !e.view.Busy && !e.consumed && !e.view.OutcomeUnknown {
			e.view.KeptPaused = true
			return nil
		}
	}
	return errors.New("browser continuation is no longer pending")
}

func (c *Coordinator) Displayed(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.views {
		if e.view.ID == id {
			e.view.Displayed = true
		}
	}
}

// Refresh is a separate, explicit UI action, not an automatic retry. An
// uncertain prior request can never be replaced with a new request ID.
func (c *Coordinator) Refresh(ctx context.Context, id string) error {
	c.Poll(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.views {
		if !c.closed && e.view.ID == id && e.state.Paused && !e.view.Busy && !e.consumed && !e.view.OutcomeUnknown {
			e.view.ID, e.view.ExpiresAt = rand.Text(), c.now().Add(5*time.Minute)
			e.view.Error, e.view.NeedsRefresh, e.view.Displayed = "", false, false
			return nil
		}
	}
	return errors.New("browser continuation target changed or its result is still unknown")
}

func request(e *entry) browserhost.ContinueRequest {
	return browserhost.ContinueRequest{BrowserID: e.browser, InstanceEpoch: e.epoch, PauseGeneration: e.view.Generation, RequestID: e.view.ID, ExpiresAt: e.view.ExpiresAt}
}

func (c *Coordinator) recordOutcome(e *entry, outcome string) {
	switch outcome {
	case "resumed":
		e.consumed, e.view.OutcomeUnknown, e.view.Error = true, false, ""
	case "not_consumed":
		e.view.OutcomeUnknown, e.view.NeedsRefresh = false, true
		e.view.Error = "Resume was not applied. Refresh this confirmation to retry."
	default:
		e.view.OutcomeUnknown = true
		e.view.Error = "Resume outcome unknown; checking the receipt without resending"
	}
}

// Continue is callable only from the local user input path. It sends one
// authenticated request, then queries a receipt rather than replaying on error.
func (c *Coordinator) Continue(ctx context.Context, id string) error {
	c.mu.Lock()
	var e *entry
	for _, candidate := range c.views {
		if candidate.view.ID == id {
			e = candidate
			break
		}
	}
	if c.closed || e == nil || !e.state.Paused || e.view.Busy || e.consumed || e.view.OutcomeUnknown {
		c.mu.Unlock()
		return errors.New("browser continuation is no longer pending or its outcome is unknown")
	}
	if !e.view.Supported {
		c.mu.Unlock()
		return errors.New("browser_runtime_capability_missing: upgrade the browser host")
	}
	if c.now().After(e.view.ExpiresAt) || e.view.NeedsRefresh {
		c.mu.Unlock()
		return errors.New("browser_continuation_expired: refresh this request")
	}
	if e.view.RetryAfterMS > 0 {
		c.mu.Unlock()
		return errors.New("browser_rate_limited: server Retry-After has not elapsed")
	}
	e.view.Busy = true
	e.view.KeptPaused = false
	req := request(e)
	c.mu.Unlock()
	op, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.lifetime, cancel)
	defer stop()
	err := e.host.ContinueChecked(op, req)
	outcome := "resumed"
	if err != nil {
		query, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		var queryErr error
		outcome, queryErr = e.host.ContinuationOutcome(query, req)
		cancel()
		if queryErr == nil && outcome == "resumed" {
			err = nil
		} else if queryErr != nil {
			outcome = "unknown"
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.views[e.key] != e || e.view.ID != id {
		return errors.New("browser continuation target changed")
	}
	e.view.Busy = false
	c.recordOutcome(e, outcome)
	e.nextPoll = time.Time{} // read back immediately after a local decision
	if err != nil {
		return errors.New(e.view.Error)
	}
	// Poll is the sole producer of ready events, including pane-origin resumes.
	return nil
}

func ConfirmationText(text string) bool {
	switch text {
	case "继续", "已授权", "已授权，继续", "continue":
		return true
	}
	return false
}
