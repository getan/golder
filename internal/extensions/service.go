package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getan/golder/extension"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/mcpauth"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/pluginwire"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

type Item struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	URI         string `json:"uri,omitempty"`
}
type Catalog struct {
	Protocol  string               `json:"protocol,omitempty"`
	Version   string               `json:"version,omitempty"`
	Tools     []extension.ToolSpec `json:"tools"`
	Commands  []Item               `json:"commands,omitempty"`
	Events    []string             `json:"events,omitempty"`
	Prompts   []Item               `json:"prompts,omitempty"`
	Resources []Item               `json:"resources,omitempty"`
}
type ToolView struct {
	Spec      extension.ToolSpec `json:"spec"`
	ModelName string             `json:"model_name"`
	State     string             `json:"state"`
	Reason    string             `json:"reason,omitempty"`
}
type Snapshot struct {
	Ref            SourceRef `json:"source"`
	Type           string    `json:"type"`
	Enabled        bool      `json:"enabled"`
	Trust          string    `json:"trust"`
	State          string    `json:"state"`
	Authentication string    `json:"authentication"`
	Endpoint       string    `json:"endpoint,omitempty"`
	Version        string    `json:"version,omitempty"`
	Protocol       string    `json:"protocol,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	// LastErrorScope and LastErrorAt describe the last failed attempt
	// (connect, call, catalog or event) and when it happened. They are history:
	// State is the current readiness, and a successful attempt clears all three.
	LastErrorScope string     `json:"last_error_scope,omitempty"`
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`
	// Busy is a live, point-in-time fact for call-scoped sources: the shared
	// login-state lock is held right now (by this process or another session).
	Busy         bool           `json:"busy,omitempty"`
	Tools        []ToolView     `json:"tools"`
	Commands     []Item         `json:"commands,omitempty"`
	Events       []string       `json:"events,omitempty"`
	Prompts      []Item         `json:"prompts,omitempty"`
	Resources    []Item         `json:"resources,omitempty"`
	Revision     uint64         `json:"revision"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
}
type persisted struct {
	Approval    string  `json:"approval"`
	Fingerprint string  `json:"fingerprint"`
	Catalog     Catalog `json:"catalog"`
}
type backend interface {
	Catalog(context.Context) (Catalog, error)
	Call(context.Context, extension.Call, func(extension.Progress)) (extension.Result, error)
	GetPrompt(context.Context, string, map[string]string) (string, error)
	ReadResource(context.Context, string) (string, error)
	Close() error
}
type source struct {
	mu                              sync.Mutex // serializes connection ownership and calls, not revocation
	ref                             SourceRef
	cfg                             SourceConfig
	fingerprint                     string
	approval                        string
	storedApproval                  string
	catalog                         Catalog
	state, auth, lastError, version string
	lastErrorScope                  string
	lastErrorAt                     time.Time
	busy                            bool
	conn                            backend
	logs                            logBuffer
	enabled                         atomic.Bool
	dirty                           atomic.Bool
	cancel                          context.CancelFunc
	interrupt                       atomic.Pointer[context.CancelFunc]
	life                            context.Context
	snapshot                        atomic.Pointer[Snapshot]
}
type Options struct {
	Cwd           string
	ConfigPath    string
	Allow         func(string) bool
	Permissions   *permissions.State
	NoTools       bool
	IgnoreSources bool // embedders opt in separately from built-in tools
}
type Manager struct {
	mu        sync.RWMutex
	sources   map[string]*source
	opts      Options
	ctx       context.Context
	cancel    context.CancelFunc
	revision  atomic.Uint64
	closed    atomic.Bool
	persistMu sync.Mutex
	skills    skillSet
	events    chan pluginwire.EventParams
}

func New(opts Options) (*Manager, error) {
	if opts.Cwd == "" {
		opts.Cwd, _ = os.Getwd()
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = ConfigPath()
	}
	if opts.ConfigPath == "" {
		return nil, errors.New("golder state root unavailable")
	}
	var cfg Config
	if !opts.IgnoreSources {
		var e error
		cfg, e = LoadConfig(opts.ConfigPath)
		if e != nil {
			return nil, e
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{sources: map[string]*source{}, opts: opts, ctx: ctx, cancel: cancel, events: make(chan pluginwire.EventParams, 64)}
	m.revision.Store(1)
	var saved map[string]persisted
	if b, e := readLimited(m.statePath(), 16<<20); e == nil {
		_ = json.Unmarshal(b, &saved)
	}
	add := func(ref SourceRef, c SourceConfig) {
		s := m.makeSource(ref, c, saved)
		m.sources[ref.String()] = s
		m.publish(s)
	}
	for id, c := range cfg.Plugins {
		add(SourceRef{Kind: "plugin", ID: id}, c)
	}
	for id, c := range cfg.MCP.Servers {
		add(SourceRef{Kind: "mcp", ID: id}, c)
	}
	// Legacy directory scanning is intentionally data-only. No initialize call.
	if dir := golderhome.File(golderhome.PluginsDir); dir != "" && !opts.IgnoreSources {
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			info, e := entry.Info()
			if e != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || !idPattern.MatchString(entry.Name()) {
				continue
			}
			ref := SourceRef{Kind: "plugin", ID: entry.Name()}
			if _, ok := m.sources[ref.String()]; !ok {
				add(ref, SourceConfig{Runtime: "rpc", Command: filepath.Join(dir, entry.Name())})
			}
		}
	}
	m.revision.Store(1)
	go m.deliverEvents()
	return m, nil
}

// makeSource builds the source for one declaration, with the approval and
// catalog another process saved for it.
func (m *Manager) makeSource(ref SourceRef, c SourceConfig, saved map[string]persisted) *source {
	if c.StateDir {
		c.stateHome = sourceHome(ref)
	}
	s := &source{ref: ref, cfg: c, state: "disconnected", auth: "not-required"}
	s.logs.registerConfigSecrets(c)
	s.enabled.Store(c.Enabled && !m.opts.NoTools)
	var fp string
	err := validateConfig(ref, c)
	if err == nil {
		fp, err = fingerprint(ref, c)
	}
	s.fingerprint = fp
	if fp != "" && ref.Kind == "mcp" && c.Transport == "http" && !hasAuthorizationHeader(c.Headers) {
		oauthFP, authErr := fingerprint(ref, c)
		if authErr == nil {
			_, authErr = mcpauth.Stored(ref.String(), oauthFP, c.URL, s.logs.AddSecrets)
		}
		if authErr != nil {
			s.logs.mu.Lock()
			s.logs.unresolved = true
			s.logs.mu.Unlock()
		}
	}
	if err != nil {
		s.state = "failed"
		s.recordFailure("config", clean(err.Error(), 2048), time.Now())
	}
	if p, ok := saved[ref.String()]; ok {
		s.approval = p.Approval
		s.storedApproval = p.Approval
		if p.Fingerprint == fp && fp != "" && s.logs.safeCatalog(p.Catalog) {
			s.catalog = p.Catalog
		}
	}
	if len(c.Headers) > 0 || len(c.Env) > 0 {
		s.auth = "configured"
	}
	if c.Runtime == "cli" {
		if mf, e := loadManifest(expandPath(c.Manifest)); e == nil {
			s.catalog.Tools = nil
			for _, t := range mf.Tools {
				s.catalog.Tools = append(s.catalog.Tools, specOf(t))
			}
			s.version = mf.Plugin.Version
			if err == nil {
				s.state = "on demand"
			}
		}
	}
	return s
}

// ReloadSources reads the configuration again and adds the sources another
// process wrote since this Manager was made: the GUI started before the TUI
// installed one. Sources already known stay as they are, with their live
// state and approval; none is removed.
func (m *Manager) ReloadSources() error {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	var cfg Config
	if !m.opts.IgnoreSources {
		var err error
		cfg, err = LoadConfig(m.opts.ConfigPath)
		if err != nil {
			return err
		}
	}
	var saved map[string]persisted
	if b, err := readLimited(m.statePath(), 16<<20); err == nil {
		_ = json.Unmarshal(b, &saved)
	}
	var added []*source
	m.mu.Lock()
	add := func(ref SourceRef, c SourceConfig) {
		key := ref.String()
		if _, ok := m.sources[key]; ok {
			return
		}
		s := m.makeSource(ref, c, saved)
		m.sources[key] = s
		added = append(added, s)
	}
	for id, c := range cfg.Plugins {
		add(SourceRef{Kind: "plugin", ID: id}, c)
	}
	for id, c := range cfg.MCP.Servers {
		add(SourceRef{Kind: "mcp", ID: id}, c)
	}
	m.mu.Unlock()
	for _, s := range added {
		m.publish(s)
	}
	if len(added) > 0 {
		m.revision.Add(1)
	}
	return nil
}

func (m *Manager) statePath() string {
	return filepath.Join(filepath.Dir(m.opts.ConfigPath), "extensions-state.json")
}
func (m *Manager) get(ref SourceRef) (*source, error) {
	m.mu.RLock()
	s := m.sources[ref.String()]
	m.mu.RUnlock()
	if s == nil {
		return nil, fmt.Errorf("unknown extension %s", ref)
	}
	return s, nil
}
func (m *Manager) Config(ref SourceRef) (SourceConfig, error) {
	s, e := m.get(ref)
	if e != nil {
		return SourceConfig{}, e
	}
	var cfg SourceConfig
	b, _ := json.Marshal(s.cfg)
	_ = json.Unmarshal(b, &cfg)
	return cfg, nil
}
func (m *Manager) Cwd() string { return m.opts.Cwd }

func (m *Manager) save() error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	data := map[string]persisted{}
	if b, err := readLimited(m.statePath(), 16<<20); err == nil {
		_ = json.Unmarshal(b, &data)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for key, s := range m.sources {
		s.mu.Lock()
		data[key] = persisted{s.storedApproval, s.fingerprint, s.catalog}
		s.mu.Unlock()
	}
	b, e := json.MarshalIndent(data, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(m.statePath(), b)
}

func (m *Manager) Approve(ref SourceRef) error {
	return m.approve(ref, true)
}

// ApproveSession must not become persistent when a later probe saves its
// catalog. Session admission and remembered trust are separate state.
func (m *Manager) ApproveSession(ref SourceRef) error { return m.approve(ref, false) }

func (m *Manager) approve(ref SourceRef, remember bool) error {
	if m.opts.NoTools {
		return errors.New("extensions disabled for this run")
	}
	if m.closed.Load() {
		return errors.New("extension manager closed")
	}
	s, e := m.get(ref)
	if e != nil {
		return e
	}
	if e = validateConfig(ref, s.cfg); e != nil {
		return e
	}
	fp, e := fingerprint(ref, s.cfg)
	if e != nil {
		return e
	}
	s.mu.Lock()
	if fp != s.fingerprint {
		s.mu.Unlock()
		return errors.New("extension changed since review; reload its configuration before approving")
	}
	s.approval = fp
	if remember {
		s.storedApproval = fp
	}
	s.fingerprint = fp
	m.revision.Add(1)
	m.publish(s)
	s.mu.Unlock()
	if remember {
		return m.save()
	}
	return nil
}

func (m *Manager) Enable(ref SourceRef) error {
	if m.opts.NoTools || m.closed.Load() {
		return errors.New("extensions disabled for this run")
	}
	s, e := m.get(ref)
	if e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := s.approval != "" && s.approval == s.fingerprint
	if !ok {
		return errors.New("extension requires explicit trust approval")
	}
	s.enabled.Store(true)
	m.revision.Add(1)
	m.publish(s)
	return nil
}

func (m *Manager) Disable(ref SourceRef) error {
	s, e := m.get(ref)
	if e != nil {
		return e
	}
	s.enabled.Store(false)
	m.revision.Add(1)
	if cancel := s.interrupt.Load(); cancel != nil {
		(*cancel)()
	}
	// Admission is revoked before teardown; callers with old tools cannot enter.
	go func() { _ = m.Disconnect(ref) }()
	return nil
}

func (m *Manager) Disconnect(ref SourceRef) error {
	s, e := m.get(ref)
	if e != nil {
		return e
	}
	if cancel := s.interrupt.Load(); cancel != nil {
		(*cancel)()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	if s.cfg.Runtime == "cli" {
		s.state = "on demand"
	} else {
		s.state = "disconnected"
	}
	m.publish(s)
	return nil
}

func (m *Manager) connectLocked(ctx context.Context, s *source) error {
	return m.connectLockedWithOAuth(ctx, s, nil, false)
}

func (m *Manager) connectLockedWithOAuth(ctx context.Context, s *source, oauth auth.OAuthHandler, interactive bool) error {
	defer m.publish(s)
	if e := validateConfig(s.ref, s.cfg); e != nil {
		return e
	}
	if m.closed.Load() || m.opts.NoTools || !s.enabled.Load() {
		return errors.New("extension disabled; not executed")
	}
	fp, e := fingerprint(s.ref, s.cfg)
	if e != nil {
		return e
	}
	if fp == "" || fp != s.approval || fp != s.fingerprint {
		return errors.New("extension changed or is not approved; not executed")
	}
	if s.conn != nil {
		return nil
	}
	s.logs.registerConfigSecrets(s.cfg)
	if s.ref.Kind == "mcp" && s.cfg.Transport == "http" && oauth == nil && !hasAuthorizationHeader(s.cfg.Headers) {
		oauthFP, authErr := fingerprint(s.ref, s.cfg)
		if authErr != nil {
			return authErr
		}
		oauth, e = mcpauth.Stored(s.ref.String(), oauthFP, s.cfg.URL, s.logs.AddSecrets)
		if e != nil {
			return e
		}
	}
	s.state = "connecting"
	m.publish(s)
	life, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	s.life = life
	s.interrupt.Store(&cancel)
	defaultInitTimeout := 15 * time.Second
	if callScopedLarkState(s.cfg) {
		// Waiting for a state lease is not a stalled MCP handshake. The helper
		// handshake gets its own startup budget after the lease is acquired.
		defaultInitTimeout = timeout(s.cfg.CallTimeout, 120*time.Second)
	}
	initTimeout := timeout(s.cfg.StartupTimeout, defaultInitTimeout)
	if interactive {
		initTimeout = 5 * time.Minute
	}
	initCtx, stop := context.WithTimeout(ctx, initTimeout)
	defer stop()
	var b backend
	if callScopedLarkState(s.cfg) {
		b = newLarkStateBackend(life, s.cfg, m.opts.Cwd, &s.logs, func() { s.dirty.Store(true) }, func() error {
			if err := life.Err(); err != nil {
				return err
			}
			if m.closed.Load() || m.opts.NoTools || !s.enabled.Load() {
				return errors.New("extension disabled; not executed")
			}
			current, err := fingerprint(s.ref, s.cfg)
			if err != nil {
				return err
			}
			// fp is the immutable approval snapshot for this connection. Revocation
			// disables/cancels its source; do not race mutable source fields while
			// concurrent operations wait outside source.mu.
			if current == "" || current != fp {
				return errors.New("extension or dependencies changed while waiting; review before execution")
			}
			return nil
		})
	} else {
		b, e = openBackend(initCtx, life, s.cfg, m.opts.Cwd, &s.logs, func() { s.dirty.Store(true) }, oauth)
	}
	if e != nil {
		cancel()
		s.state = "failed"
		s.recordFailure("connect", s.logs.Clean(e.Error(), 2048), time.Now())
		failure := strings.ToLower(e.Error())
		if strings.Contains(failure, "authentication") || strings.Contains(failure, "unauthorized") || strings.Contains(failure, "401") {
			s.auth = "required"
		}
		return errors.New(s.lastError)
	}
	cat, e := b.Catalog(initCtx)
	if e != nil {
		cancel()
		_ = b.Close()
		s.state = "failed"
		s.recordFailure("connect", s.logs.Clean(e.Error(), 2048), time.Now())
		return errors.New(s.lastError)
	}
	if !s.logs.safeCatalog(cat) {
		cancel()
		_ = b.Close()
		s.state = "failed"
		s.recordFailure("catalog", "extension catalog contains a credential and was withheld", time.Now())
		return errors.New(s.lastError)
	}
	if !s.enabled.Load() || m.closed.Load() {
		cancel()
		_ = b.Close()
		return errors.New("extension disabled during connect")
	}
	s.conn = b
	s.catalog = cat
	s.state = "ready"
	if s.cfg.Runtime == "cli" || callScopedLarkState(s.cfg) {
		s.state = "on demand"
	}
	s.clearFailure()
	if oauth != nil {
		if tokenSource, err := oauth.TokenSource(ctx); err == nil && tokenSource != nil {
			s.auth = "verified"
		}
	}
	s.dirty.Store(false)
	m.revision.Add(1)
	return nil
}

func (m *Manager) Connect(ctx context.Context, ref SourceRef) error {
	s, e := m.get(ref)
	if e != nil {
		return e
	}
	s.mu.Lock()
	e = m.connectLocked(ctx, s)
	s.mu.Unlock()
	if e != nil {
		return e
	}
	return m.save()
}

func hasAuthorizationHeader(headers map[string]string) bool {
	for name := range headers {
		if strings.EqualFold(name, "Authorization") {
			return true
		}
	}
	return false
}

// AuthorizeMCP runs an explicitly requested, operator-controlled OAuth flow.
// It is never called by tool execution or ordinary connection attempts.
func (m *Manager) AuthorizeMCP(ctx context.Context, ref SourceRef, open mcpauth.Open) error {
	s, err := m.get(ref)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if ref.Kind != "mcp" || s.cfg.Transport != "http" {
		s.mu.Unlock()
		return errors.New("OAuth authorization requires a remote HTTP MCP server")
	}
	if hasAuthorizationHeader(s.cfg.Headers) {
		s.mu.Unlock()
		return errors.New("remove the configured Authorization header before using OAuth")
	}
	if !s.enabled.Load() || s.approval == "" || s.approval != s.fingerprint {
		s.mu.Unlock()
		return errors.New("review and enable the MCP server before authorizing")
	}
	if s.conn != nil {
		s.mu.Unlock()
		return errors.New("disconnect the MCP server before authorizing again")
	}
	// The credential belongs to the same account-scoped HTTP definition across
	// projects; the full config and declared executable bytes remain bound.
	oauthFP, err := fingerprint(ref, s.cfg)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	flow, err := mcpauth.NewWithClient(ref.String(), oauthFP, s.cfg.URL, mcpauth.Registration{ClientID: s.cfg.OAuthClientID, Issuer: s.cfg.OAuthIssuer}, open, s.logs.AddSecrets)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	err = m.connectLockedWithOAuth(ctx, s, flow, true)
	flow.Close()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return m.save()
}

func (m *Manager) Refresh(ctx context.Context, ref SourceRef) error {
	s, e := m.get(ref)
	if e != nil {
		return e
	}
	s.mu.Lock()
	e = m.connectLocked(ctx, s)
	if e == nil {
		var cat Catalog
		cat, e = s.conn.Catalog(ctx)
		if e == nil && !s.logs.safeCatalog(cat) {
			e = errors.New("extension catalog contains a credential and was withheld")
		}
		if e == nil {
			s.catalog = cat
			s.dirty.Store(false)
			m.revision.Add(1)
		}
	}
	m.publish(s)
	s.mu.Unlock()
	if e != nil {
		return e
	}
	return m.save()
}
func (m *Manager) Logs(ref SourceRef) (string, error) {
	s, e := m.get(ref)
	if e != nil {
		return "", e
	}
	return s.logs.String(), nil
}

func allowed(c SourceConfig, name string) bool {
	match := func(patterns []string) bool {
		for _, p := range patterns {
			if ok, _ := filepath.Match(p, name); ok {
				return true
			}
		}
		return false
	}
	return !match(c.DisallowedTools) && (len(c.AllowedTools) == 0 || match(c.AllowedTools))
}
func (m *Manager) views(s *source) []ToolView {
	out := make([]ToolView, 0, len(s.catalog.Tools))
	counts := map[string]int{}
	for _, spec := range s.catalog.Tools {
		counts[ModelName(s.ref, spec.Name)]++
	}
	for _, spec := range s.catalog.Tools {
		spec.Description = clean(spec.Description, 4096)
		v := ToolView{Spec: spec, ModelName: ModelName(s.ref, spec.Name), State: "available"}
		if spec.Name == "" || counts[v.ModelName] > 1 {
			v.State = "invalid"
			v.Reason = "empty or duplicate tool name"
		} else if _, e := compileSchema(spec.InputSchema); e != nil {
			v.State = "invalid"
			v.Reason = clean(e.Error(), 512)
		} else if !m.toolAllowed(s, spec.Name) {
			v.State = "blocked"
			v.Reason = "tool policy"
		} else if !s.enabled.Load() {
			v.State = "disabled"
		} else if s.approval == "" || s.approval != s.fingerprint {
			v.State = "needs-review"
		}
		out = append(out, v)
	}
	return out
}

// publish is called with source.mu held (or during construction). Readers never
// wait on an extension that is busy executing a tool or establishing a session.
func (m *Manager) publish(s *source) {
	typ := "CLI"
	if s.ref.Kind == "mcp" {
		typ = "MCP · " + s.cfg.Transport
	} else if s.cfg.Runtime == "rpc" {
		typ = "RPC · legacy"
	}
	trust := "needs-review"
	if s.approval != "" && s.approval == s.fingerprint {
		trust = "approved"
	}
	endpoint := s.cfg.Command
	if s.cfg.Transport == "http" {
		endpoint = s.cfg.URL
	}
	if s.cfg.Runtime == "cli" {
		endpoint = s.cfg.Manifest
	}
	version := s.catalog.Version
	if version == "" {
		version = s.version
	}
	capabilities := map[string]any{"network": s.cfg.Network || s.cfg.Transport == "http", "state_dir": s.cfg.StateDir, "executables": s.cfg.Executables, "skills": s.cfg.Skills, "setup_origins": s.cfg.SetupOrigins, "confirm_tools": s.cfg.ConfirmTools, "aggregate_tools": s.cfg.AggregateTools}
	if len(s.cfg.Setup) > 0 {
		capabilities["setup"] = clean(strings.Join(s.cfg.Setup, " "), 2048)
	}
	variants := make(map[string]string, len(s.cfg.SetupVariants))
	for name, argv := range s.cfg.SetupVariants {
		variants[name] = clean(strings.Join(argv, " "), 2048)
	}
	capabilities["setup_variants"] = variants
	var lastErrorAt *time.Time
	if !s.lastErrorAt.IsZero() {
		at := s.lastErrorAt
		lastErrorAt = &at
	}
	s.snapshot.Store(&Snapshot{
		Ref: s.ref, Type: typ, Enabled: s.enabled.Load(), Trust: trust,
		State: s.state, Authentication: s.auth,
		Endpoint: clean(endpoint, 512), Version: clean(version, 128), Protocol: clean(s.catalog.Protocol, 128),
		LastError: s.lastError, LastErrorScope: s.lastErrorScope, LastErrorAt: lastErrorAt, Busy: s.busy,
		Tools: m.views(s), Commands: s.catalog.Commands, Events: s.catalog.Events,
		Prompts: s.catalog.Prompts, Resources: s.catalog.Resources,
		Revision: m.revision.Load(), Capabilities: capabilities,
	})
}

// recordFailure stores the history of one failed attempt. It deliberately does
// not decide State: the caller knows whether the failure actually changed what
// the source can do now (a connect failure does; a single call failure on a
// call-scoped source does not).
func (s *source) recordFailure(scope, message string, at time.Time) {
	s.lastError, s.lastErrorScope, s.lastErrorAt = message, scope, at
}

// clearFailure forgets the last attempt after one succeeded.
func (s *source) clearFailure() {
	s.lastError, s.lastErrorScope, s.lastErrorAt = "", "", time.Time{}
}

// noteCallFailure records a failed tool call. Readiness changes only for a
// persistent source, whose connection is gone; a call-scoped source (Lark)
// starts a fresh helper per operation, so it stays ready for the next call.
func (s *source) noteCallFailure(message string, callScoped bool, at time.Time) {
	s.recordFailure("call", message, at)
	if !callScoped {
		s.state = "failed"
	}
}

// LastAttemptText renders the history of the last failed attempt for a status
// view: when it happened, what kind of attempt it was, and the error. State is
// the current readiness, so this text never stands in for it.
func LastAttemptText(s Snapshot) string {
	if s.LastError == "" {
		return ""
	}
	var b strings.Builder
	if s.LastErrorAt != nil {
		b.WriteString(s.LastErrorAt.Local().Format("2006-01-02 15:04"))
		b.WriteString(" · ")
	}
	if s.LastErrorScope != "" {
		b.WriteString(s.LastErrorScope)
		b.WriteString(": ")
	}
	b.WriteString(s.LastError)
	return b.String()
}

func (m *Manager) List() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Snapshot, 0, len(m.sources))
	for _, s := range m.sources {
		if p := s.snapshot.Load(); p != nil {
			var snap Snapshot
			b, _ := json.Marshal(p)
			_ = json.Unmarshal(b, &snap)
			snap.Enabled = s.enabled.Load()
			if !snap.Enabled {
				snap.State = "disabled"
				for i := range snap.Tools {
					if snap.Tools[i].State == "available" {
						snap.Tools[i].State = "disabled"
					}
				}
			}
			out = append(out, snap)
		}
	}
	slices.SortFunc(out, func(a, b Snapshot) int { return strings.Compare(a.Ref.String(), b.Ref.String()) })
	return out
}

// RefreshLiveState re-reads the cheap facts a published snapshot cannot know:
// for a call-scoped source, whether its shared login-state lock is held right
// now by this or another session. It starts no child process and makes no
// network call, so a list view may call it on open. Personal authorization is
// still the explicit, operator-initiated ProbeLarkAuthorization.
func (m *Manager) RefreshLiveState() {
	if m == nil || m.closed.Load() {
		return
	}
	m.mu.RLock()
	sources := make([]*source, 0, len(m.sources))
	for _, s := range m.sources {
		sources = append(sources, s)
	}
	m.mu.RUnlock()
	for _, s := range sources {
		if !callScopedLarkState(s.cfg) {
			continue
		}
		busy := larkStateLockBusy(s.cfg.stateHome)
		if !s.mu.TryLock() {
			// An operation owns the source right now; its own transitions will
			// publish what it is doing.
			continue
		}
		if s.busy != busy {
			s.busy = busy
			m.publish(s)
		}
		s.mu.Unlock()
	}
}

func (m *Manager) Tools() []agentcore.AgentTool {
	var out []agentcore.AgentTool
	for _, snap := range m.List() {
		origin, err := m.get(snap.Ref)
		if err != nil {
			continue
		}
		for _, v := range snap.Tools {
			if v.State == "available" {
				out = append(out, &tool{manager: m, ref: snap.Ref, origin: origin, spec: v.Spec, name: v.ModelName})
			}
		}
	}
	return out
}

// PackageSkills returns only approved enabled package roots. Independent skills
// retain their existing discovery order and win same-name collisions upstream.
func (m *Manager) PackageSkills() map[string]string {
	out := map[string]string{}
	for _, snap := range m.List() {
		if !snap.Enabled || snap.Trust != "approved" {
			continue
		}
		s, _ := m.get(snap.Ref)
		if s.cfg.Skills != "" {
			if fp, err := fingerprint(s.ref, s.cfg); err == nil && fp == s.approval {
				out[snap.Ref.String()] = expandPath(s.cfg.Skills)
			}
			continue
		}
		if s.cfg.Manifest == "" {
			continue
		}
		mf, e := loadManifest(expandPath(s.cfg.Manifest))
		if e != nil || mf.Plugin.Skills == "" || filepath.IsAbs(mf.Plugin.Skills) {
			continue
		}
		base, _ := filepath.EvalSymlinks(filepath.Dir(expandPath(s.cfg.Manifest)))
		dir, e := filepath.EvalSymlinks(filepath.Join(base, mf.Plugin.Skills))
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(base, dir)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			out[snap.Ref.String()] = dir
		}
	}
	return out
}

func (m *Manager) Prompt(ctx context.Context, ref SourceRef, name string, args map[string]string) (string, error) {
	s, e := m.get(ref)
	if e != nil {
		return "", e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e = m.connectLocked(ctx, s); e != nil {
		return "", e
	}
	result, err := s.conn.GetPrompt(ctx, name, args)
	if err != nil {
		return "", errors.New(s.logs.Clean(err.Error(), 2048))
	}
	return s.logs.Clean(result, outputLimit), nil
}
func (m *Manager) Resource(ctx context.Context, ref SourceRef, uri string) (string, error) {
	s, e := m.get(ref)
	if e != nil {
		return "", e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e = m.connectLocked(ctx, s); e != nil {
		return "", e
	}
	result, err := s.conn.ReadResource(ctx, uri)
	if err != nil {
		return "", errors.New(s.logs.Clean(err.Error(), 2048))
	}
	return s.logs.Clean(result, outputLimit), nil
}

func (m *Manager) Close() error {
	if !m.closed.CompareAndSwap(false, true) {
		return nil
	}
	m.cancel()
	m.mu.RLock()
	var refs []SourceRef
	for _, s := range m.sources {
		s.enabled.Store(false)
		refs = append(refs, s.ref)
	}
	m.mu.RUnlock()
	for _, ref := range refs {
		_ = m.Disconnect(ref)
	}
	return nil
}
