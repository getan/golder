package hostcompat

import (
	"context"
	"errors"
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
	// Present the pane token where the daemon takes one. It places this
	// connection in the pane the report is about even where the kernel cannot
	// (a wrapper, a nested shell), which is what keeps a report off a
	// neighbour's window. A refused proof changes nothing: the daemon then
	// holds the connection to what it can prove, and the report names the pane
	// anyway.
	if h.opts.Proof != "" {
		_, _ = client.Call(ctx, "pane-grants", map[string]any{
			"pane_id": h.opts.PaneID, "pane_token": h.opts.Proof,
		})
	}
	h.client = client
	return client, nil
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
