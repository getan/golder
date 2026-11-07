package extensions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/getan/golder/internal/filelock"
)

// MCPEdit is an operator-only snapshot. Config can contain legacy literal
// secrets: callers may show keys, but must never render or log Env/Headers.
type MCPEdit struct {
	ID       string
	Revision string
	Config   SourceConfig
}

func mcpConfigRevision(cfg SourceConfig) string {
	raw, _ := json.Marshal(cfg)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func sameMCPDefinition(a, b SourceConfig) bool {
	a.Enabled, b.Enabled = false, false
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// PrepareMCPEdit reads the current disk definition and refuses to edit a
// source whose in-memory definition differs from a different process's edit.
func (m *Manager) PrepareMCPEdit(id string) (MCPEdit, error) {
	if m == nil || m.closed.Load() {
		return MCPEdit{}, errors.New("extension manager unavailable")
	}
	ref := SourceRef{Kind: "mcp", ID: id}
	active, err := m.Config(ref)
	if err != nil {
		return MCPEdit{}, err
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	unlock, err := filelock.Acquire(m.opts.ConfigPath)
	if err != nil {
		return MCPEdit{}, err
	}
	defer unlock()
	current, err := LoadConfig(m.opts.ConfigPath)
	if err != nil {
		return MCPEdit{}, err
	}
	cfg, ok := current.MCP.Servers[id]
	if !ok || !sameMCPDefinition(cfg, active) {
		return MCPEdit{}, errors.New("MCP configuration changed in another process; reload or restart before editing")
	}
	return MCPEdit{ID: id, Revision: mcpConfigRevision(cfg), Config: cfg}, nil
}

// EnableReviewedMCP approves exactly the definition reviewed by the operator.
// The disk comparison and optional enabled-bit write share one interprocess
// lock, so a concurrent settings edit cannot be silently overwritten.
func (m *Manager) EnableReviewedMCP(edit MCPEdit, remember bool) error {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	unlock, err := filelock.Acquire(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := LoadConfig(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	cfg, ok := current.MCP.Servers[edit.ID]
	if !ok || mcpConfigRevision(cfg) != edit.Revision {
		return errors.New("MCP configuration changed since review; restart and review again")
	}
	ref := SourceRef{Kind: "mcp", ID: edit.ID}
	active, err := m.Config(ref)
	if err != nil || !sameMCPDefinition(cfg, active) {
		return errors.New("MCP configuration changed in another process; restart and review again")
	}
	if remember {
		err = m.Approve(ref)
	} else {
		err = m.ApproveSession(ref)
	}
	if err != nil {
		return err
	}
	if remember {
		cfg.Enabled = true
		if err := writeSourceLocked(m.opts.ConfigPath, ref, &cfg); err != nil {
			return err
		}
	}
	return m.Enable(ref)
}

// DisableReviewedMCP refuses to overwrite a newer source definition while
// persisting a disabled bit. Revocation precedes the disk update.
func (m *Manager) DisableReviewedMCP(edit MCPEdit) error {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	unlock, err := filelock.Acquire(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := LoadConfig(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	cfg, ok := current.MCP.Servers[edit.ID]
	if !ok || mcpConfigRevision(cfg) != edit.Revision {
		return errors.New("MCP configuration changed since review; restart and review again")
	}
	ref := SourceRef{Kind: "mcp", ID: edit.ID}
	active, err := m.Config(ref)
	if err != nil || !sameMCPDefinition(cfg, active) {
		return errors.New("MCP configuration changed in another process; restart and review again")
	}
	if err := m.Disable(ref); err != nil {
		return err
	}
	cfg.Enabled = false
	return writeSourceLocked(m.opts.ConfigPath, ref, &cfg)
}

// ReplaceReviewedMCP atomically replaces a complete reviewed definition. The
// new configuration is disabled and must receive a new approval.
func (m *Manager) ReplaceReviewedMCP(edit MCPEdit, cfg SourceConfig) error {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	ref := SourceRef{Kind: "mcp", ID: edit.ID}
	cfg.Enabled = false
	if err := validateConfig(ref, cfg); err != nil {
		return err
	}
	if _, err := fingerprint(ref, cfg, m.opts.Cwd); err != nil {
		return err
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	unlock, err := filelock.Acquire(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := LoadConfig(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	previous, ok := current.MCP.Servers[edit.ID]
	if !ok || mcpConfigRevision(previous) != edit.Revision {
		return errors.New("MCP configuration changed since review; restart and review again")
	}
	if err := writeSourceLocked(m.opts.ConfigPath, ref, &cfg); err != nil {
		return err
	}
	if err := m.ConfigureSource(ref, cfg); err != nil {
		return fmt.Errorf("%w: %v", ErrSourceCommitted, err)
	}
	return nil
}

// UpdateMCPTarget replaces the reviewed connection and environment/header
// values while retaining advanced source capabilities. An edit always disables
// the source and invalidates its previous approval.
func (m *Manager) UpdateMCPTarget(edit MCPEdit, draft MCPDraft) error {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	if edit.ID != draft.ID || edit.ID == "lark" || edit.Config.Transport != draft.Transport {
		return errors.New("source name and transport cannot be changed by this editor")
	}
	if err := draft.Validate(); err != nil {
		return err
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	unlock, err := filelock.Acquire(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := LoadConfig(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	cfg, ok := current.MCP.Servers[edit.ID]
	if !ok || mcpConfigRevision(cfg) != edit.Revision {
		return errors.New("MCP configuration changed since review; reload before saving")
	}
	var created []string
	committed := false
	defer func() {
		if !committed {
			removeCreatedMCPCredentials(created)
		}
	}()
	var names []string
	cfg.Env, names, err = protectMCPValues(edit.ID, draft.Env)
	created = append(created, names...)
	if err != nil {
		return err
	}
	cfg.Headers, names, err = protectMCPValues(edit.ID, draft.Headers)
	created = append(created, names...)
	if err != nil {
		return err
	}
	cfg.Enabled = false
	cfg.Command, cfg.Args, cfg.URL, cfg.Network = draft.Command, append([]string(nil), draft.Args...), draft.URL, draft.Network
	cfg.OAuthClientID, cfg.OAuthIssuer = draft.OAuthClientID, draft.OAuthIssuer
	ref := SourceRef{Kind: "mcp", ID: edit.ID}
	if err := validateConfig(ref, cfg); err != nil {
		return err
	}
	if _, err := fingerprint(ref, cfg, m.opts.Cwd); err != nil {
		return fmt.Errorf("cannot review MCP executable: %w", err)
	}
	if err := writeSourceLocked(m.opts.ConfigPath, ref, &cfg); err != nil {
		return err
	}
	committed = true
	if err := m.ConfigureSource(ref, cfg); err != nil {
		return fmt.Errorf("MCP saved disabled; reload extensions: %w", err)
	}
	return nil
}

// RemoveMCP deletes only the confirmed current source definition and live
// source. Shared named credentials and source-scoped login state are retained.
func (m *Manager) RemoveMCP(edit MCPEdit) error {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	unlock, err := filelock.Acquire(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := LoadConfig(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	cfg, ok := current.MCP.Servers[edit.ID]
	if !ok || mcpConfigRevision(cfg) != edit.Revision {
		return errors.New("MCP configuration changed since review; reload before removing")
	}
	ref := SourceRef{Kind: "mcp", ID: edit.ID}
	if err := m.Disable(ref); err != nil {
		return err
	}
	if err := m.Disconnect(ref); err != nil {
		return err
	}
	if err := writeSourceLocked(m.opts.ConfigPath, ref, nil); err != nil {
		return err
	}
	if err := m.RemoveSource(ref); err != nil {
		return fmt.Errorf("MCP removed from config; reload extensions: %w", err)
	}
	return nil
}
