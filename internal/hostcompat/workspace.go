package hostcompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/getan/golder/internal/tuiosctl"
)

// The golder-workspace dialect.
//
// golder's own workspace is the multiplexer golder starts around itself: one
// golder binary that spawns agent panes, a browser pane and the rest. Its panes
// are told so by GOLDER_WORKSPACE, and they carry the same TUIOS_* variables
// every pane of that daemon receives, because the daemon speaks tuios's
// control protocol (golder embeds that core) and the panes it starts use
// golder's own tools, which read those variables.
//
// This host therefore speaks the protocol directly, over TUIOS_SOCKET, rather
// than exec'ing a `tuios` binary. There is no such binary: the daemon is
// golder. Going through one would need it installed, would report to whatever
// daemon it happens to reach, and would put a process between the agent and its
// own workspace.
//
// The verbs are tuios's, so the state vocabulary is tuios's too (tuiosStates),
// and the conversation id rides the state report exactly as it does there. What
// differs is the transport: JSON lines on the daemon's socket, through
// internal/tuiosctl, which owns the framing, the hello handshake and the
// reconnection rules. Nothing about the wire is re-implemented here.

// workspaceClientVersion is what this host announces in hello, so a daemon's
// log says who reported.
const workspaceClientVersion = "golder-hostcompat"

// workspaceSource is the source a workspace report is filed under. It is the
// rank the external tuios CLI uses for a self-report: above the screen-reader
// sources, so a report wins over what the pane's text looks like.
const workspaceSource = "report"

// workspaceMetaTTL is how long a metadata token lives, in the same spirit as
// tuios's own feed: a report stream that stops writing leaves nothing stale on
// the rail.
const workspaceMetaTTL = 90 * time.Second

// harnessPID is the process every report names, which is this one. The daemon
// keeps the pid a pane's conversation came from and refuses a report that would
// replace it from a different known process mid-turn, so that a nested run — a
// sub-agent golder a tool call started inside this pane — cannot take the pane's
// conversation over. The guard is only as good as the pid the report carries:
// without one the daemon cannot tell the pane's own agent from a child of it,
// and it also refuses the pane's agent its own /resume or /clear while it is
// working.
func harnessPID() int { return os.Getpid() }

// workspaceHost reports to golder's own workspace daemon over its JSON socket.
type workspaceHost struct {
	opts tuiosctl.Options

	// mu guards client, which is dialed on the first report and kept for the
	// reports after it. A driver's reporter sends one batch at a time, so this
	// only has to keep the lazy dial safe, not the calls.
	mu     sync.Mutex
	client *tuiosctl.Client
}

// newWorkspace returns the workspace host for this process, or nil when the
// pane environment is missing the socket or the pane id. Resolve has already
// checked those, so nil here means the environment changed under it.
func newWorkspace(opts Options) Host {
	socket := opts.Getenv(envTuiosSocket)
	pane := opts.Getenv(envTuiosPaneID)
	if socket == "" || pane == "" {
		return nil
	}
	return &workspaceHost{opts: tuiosctl.Options{
		Socket:  socket,
		Session: opts.Getenv(envTuiosSession),
		PaneID:  pane,
		Proof:   opts.Getenv(envTuiosProof),
		Version: workspaceClientVersion,
	}}
}

func (h *workspaceHost) Name() string { return MuxGolder }

// Apply sends one state report, or the conversation alone when the report names
// no state. Both are ordinary verbs; the conversation is the state report's own
// agent_session_id there, which is what the external tuios host sends too.
func (h *workspaceHost) Apply(ctx context.Context, r Report, sess SessionRef) error {
	if r.State == "" {
		return h.setSession(ctx, sess)
	}
	state, ok := tuiosStates[r.State]
	if !ok {
		return nil
	}
	params := h.paneParams()
	params["state"] = state
	params["source"] = workspaceSource
	params["harness"] = Harness
	params["harness_pid"] = harnessPID()
	if r.Kind != KindNone {
		params["kind"] = string(r.Kind)
	}
	if r.Message != "" {
		params["message"] = r.Message
	}
	if sess.ID != "" && r.State != StateNone {
		params["agent_session_id"] = sess.ID
	}
	return h.call(ctx, "set-agent-state", params)
}

// setSession names the conversation without touching the pane's state: a
// report that named a state would move the pane, and a conversation is not a
// report about what the agent is doing.
func (h *workspaceHost) setSession(ctx context.Context, sess SessionRef) error {
	if sess.ID == "" {
		return nil
	}
	params := h.paneParams()
	params["harness"] = Harness
	params["agent_session_id"] = sess.ID
	params["harness_pid"] = harnessPID()
	return h.call(ctx, "set-agent-session", params)
}

// ApplyMeta sends display metadata as the daemon's tokens object, with the same
// keys, the same lifetime and the same cut on a value the external tuios host
// applies, so the rail reads the same either way.
func (h *workspaceHost) ApplyMeta(ctx context.Context, kv map[string]string) error {
	tokens := make(map[string]string, len(tuiosMetaKeys))
	for _, k := range tuiosMetaKeys {
		if v, ok := kv[k]; ok && v != "" {
			tokens[k] = cutRunes(v, tuiosMetaMaxValue)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	params := h.paneParams()
	params["source"] = Harness
	params["ttl_ms"] = workspaceMetaTTL.Milliseconds()
	params["tokens"] = tokens
	return h.call(ctx, "set-agent-meta", params)
}

// Release clears the pane's agent: golder is going away, and the pane goes back
// to being a shell.
func (h *workspaceHost) Release(ctx context.Context) error {
	params := h.paneParams()
	params["state"] = "none"
	params["source"] = workspaceSource
	params["harness"] = Harness
	params["harness_pid"] = harnessPID()
	return h.call(ctx, "set-agent-state", params)
}

// Close gives the daemon socket back. The reporter calls it after the last
// report has been flushed.
func (h *workspaceHost) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == nil {
		return nil
	}
	err := h.client.Close()
	h.client = nil
	return err
}

// paneParams are the fields every verb carries: which session, which pane. The
// pane is named rather than left to the daemon's idea of the caller, because a
// report is about this pane even when the person is looking at another one.
func (h *workspaceHost) paneParams() map[string]any {
	params := map[string]any{"window": h.opts.PaneID}
	if h.opts.Session != "" {
		params["session"] = h.opts.Session
	}
	return params
}

// call sends one verb over the daemon socket, dialing on first use.
//
// A failed round trip drops the connection: tuiosctl closes it on every
// transport error, so the next report dials and says hello again instead of
// reading a late reply as its own answer. A report is a snapshot of "what this
// pane is doing now", so a lost one costs nothing: the next one carries the
// same truth. A verb the daemon *declined* — an outranked report, or a nested
// run it refused as a foreign session — is not that: it answered, the stream is
// in step, and it is the daemon's decision rather than a fault to report.
func (h *workspaceHost) call(ctx context.Context, verb string, params map[string]any) error {
	client, err := h.connection(ctx)
	if err != nil {
		return err
	}
	if _, err := client.Call(ctx, verb, params); err != nil {
		var refused *tuiosctl.Error
		if !errors.As(err, &refused) {
			h.drop(client)
		}
		return err
	}
	return nil
}

// connection returns the open client, dialing when there is none.
//
// Before a byte of report goes out, the connection gives up everything but its
// own pane. That is what makes the pane named in TUIOS_PANE_ID the *only* pane
// this process can speak about: the daemon holds a connection restricted to
// scope own to the session of the pane it placed the caller in, and refuses
// every other session and every other window. Without it, a golder that cannot
// be placed in a pane — a wrapper, a nested shell, a process that left its
// pane's tree — would be an ordinary same-UID connection with the person's own
// rights, free to report "working" over a neighbour's window.
//
// The restriction fails closed. If the daemon will not bind this connection to
// this pane, no report is sent and the connection is dropped instead of kept: a
// report is bookkeeping for the terminal around the agent, and one that might
// land on somebody else's pane is worse than none at all. So a workspace this
// process cannot prove it is in reports nothing, rather than reporting with the
// rights of whoever runs the connection.
func (h *workspaceHost) connection(ctx context.Context) (*tuiosctl.Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client != nil {
		return h.client, nil
	}
	client, err := tuiosctl.Dial(ctx, h.opts)
	if err != nil {
		return nil, err
	}
	// restrict-connection is where the pane token is presented: it places this
	// connection in the pane the report is about where the kernel cannot (a
	// wrapper, a nested shell, a sub-agent that is not in the pane's process
	// tree), and it narrows the connection to that pane in the same call. Both
	// halves matter, and both are refusals: a token that does not prove the
	// claimed pane, or a caller the daemon can place in no pane at all, ends
	// here rather than falling back to full authority.
	if err := client.Restrict(ctx, false); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("hostcompat: bind this golder to workspace pane %q: %w", shortPane(h.opts.PaneID), err)
	}
	// The daemon answers which pane it settled on. A connection placed in
	// another pane than the one this process names would report about a pane it
	// is not, so it is refused here as well, even though the daemon's own
	// window check would refuse each report later.
	if got := restrictedWindow(ctx, client); got != "" && got != h.opts.PaneID {
		_ = client.Close()
		return nil, fmt.Errorf("hostcompat: the workspace placed this connection in pane %q, not in the pane this process names (%q); refusing to report",
			shortPane(got), shortPane(h.opts.PaneID))
	}
	h.client = client
	return client, nil
}

// restrictedWindow asks the daemon which pane this connection is held to, and
// returns "" when it will not say. The answer is read from pane-grants, the one
// verb a restricted connection may still call that describes it.
func restrictedWindow(ctx context.Context, client *tuiosctl.Client) string {
	// No pane_id: the question is about the caller, not about a claim, and a
	// claim would have to be proved again.
	raw, err := client.Call(ctx, "pane-grants", nil)
	if err != nil {
		return ""
	}
	var got struct {
		Pane   bool   `json:"pane"`
		Window string `json:"window"`
	}
	if json.Unmarshal(raw, &got) != nil {
		return ""
	}
	if !got.Pane {
		// The daemon placed this connection in no pane: it has no grants, and
		// the restriction it just accepted reaches no session. Reports will be
		// refused by the daemon; naming it here keeps the log readable.
		return ""
	}
	return got.Window
}

// shortPane keeps a pane id readable in an error line without pretending to be
// the whole id: a pane id is a uuid, and the first eight characters identify it
// in the daemon's own log too.
func shortPane(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// drop closes client if it is still the current one.
func (h *workspaceHost) drop(client *tuiosctl.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client != client {
		return
	}
	_ = h.client.Close()
	h.client = nil
}
