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
	"github.com/getan/golder/internal/permissions"
	legacy "github.com/getan/golder/internal/plugin"
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
	Ref            SourceRef  `json:"source"`
	Type           string     `json:"type"`
	Enabled        bool       `json:"enabled"`
	Trust          string     `json:"trust"`
	State          string     `json:"state"`
	Authentication string     `json:"authentication"`
	Endpoint       string     `json:"endpoint,omitempty"`
	Version        string     `json:"version,omitempty"`
	Protocol       string     `json:"protocol,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	Tools          []ToolView `json:"tools"`
	Commands       []Item     `json:"commands,omitempty"`
	Events         []string   `json:"events,omitempty"`
	Prompts        []Item     `json:"prompts,omitempty"`
	Resources      []Item     `json:"resources,omitempty"`
	Revision       uint64     `json:"revision"`
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
	events    chan legacy.EventParams
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
	m := &Manager{sources: map[string]*source{}, opts: opts, ctx: ctx, cancel: cancel, events: make(chan legacy.EventParams, 64)}
	m.revision.Store(1)
	var saved map[string]persisted
	if b, e := readLimited(m.statePath(), 16<<20); e == nil {
		_ = json.Unmarshal(b, &saved)
	}
	add := func(ref SourceRef, c SourceConfig) {
		s := &source{ref: ref, cfg: c, state: "disconnected", auth: "not-required"}
		s.enabled.Store(c.Enabled && !opts.NoTools)
		var fp string
		err := validateConfig(ref, c)
		if err == nil {
			fp, err = fingerprint(ref, c, opts.Cwd)
		}
		s.fingerprint = fp
		if err != nil {
			s.state = "failed"
			s.lastError = clean(err.Error(), 2048)
		}
		if p, ok := saved[ref.String()]; ok {
			s.approval = p.Approval
			s.storedApproval = p.Approval
			if p.Fingerprint == fp && fp != "" {
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
	fp, e := fingerprint(ref, s.cfg, m.opts.Cwd)
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
	defer m.publish(s)
	if e := validateConfig(s.ref, s.cfg); e != nil {
		return e
	}
	if m.closed.Load() || m.opts.NoTools || !s.enabled.Load() {
		return errors.New("extension disabled; not executed")
	}
	fp, e := fingerprint(s.ref, s.cfg, m.opts.Cwd)
	if e != nil {
		return e
	}
	if fp == "" || fp != s.approval || fp != s.fingerprint {
		return errors.New("extension changed or is not approved; not executed")
	}
	if s.conn != nil {
		return nil
	}
	s.state = "connecting"
	m.publish(s)
	life, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	s.life = life
	s.interrupt.Store(&cancel)
	initCtx, stop := context.WithTimeout(ctx, timeout(s.cfg.StartupTimeout, 15*time.Second))
	defer stop()
	b, e := openBackend(initCtx, life, s.cfg, m.opts.Cwd, &s.logs, func() { s.dirty.Store(true) })
	if e != nil {
		cancel()
		s.state = "failed"
		s.lastError = clean(e.Error(), 2048)
		if strings.Contains(e.Error(), "authentication") {
			s.auth = "required"
		}
		return errors.New(s.lastError)
	}
	cat, e := b.Catalog(initCtx)
	if e != nil {
		cancel()
		_ = b.Close()
		s.state = "failed"
		s.lastError = clean(e.Error(), 2048)
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
	if s.cfg.Runtime == "cli" {
		s.state = "on demand"
	}
	s.lastError = ""
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
		} else if !allowed(s.cfg, spec.Name) || (m.opts.Allow != nil && !m.opts.Allow(v.ModelName)) {
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
	s.snapshot.Store(&Snapshot{Ref: s.ref, Type: typ, Enabled: s.enabled.Load(), Trust: trust, State: s.state, Authentication: s.auth, Endpoint: clean(endpoint, 512), Version: clean(version, 128), Protocol: clean(s.catalog.Protocol, 128), LastError: s.lastError, Tools: m.views(s), Commands: s.catalog.Commands, Events: s.catalog.Events, Prompts: s.catalog.Prompts, Resources: s.catalog.Resources, Revision: m.revision.Load()})
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

func (m *Manager) Tools() []agentcore.AgentTool {
	var out []agentcore.AgentTool
	for _, snap := range m.List() {
		for _, v := range snap.Tools {
			if v.State == "available" {
				out = append(out, &tool{manager: m, ref: snap.Ref, spec: v.Spec, name: v.ModelName})
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
	return s.conn.GetPrompt(ctx, name, args)
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
	return s.conn.ReadResource(ctx, uri)
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
