package extensions

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// ConfigureSource installs or replaces one already-reviewed source definition
// in this Manager. It does not approve, enable or execute the source. Callers
// persist the same definition with WriteSource first, so a process crash cannot
// leave an in-memory extension that the next launch does not know about.
func (m *Manager) ConfigureSource(ref SourceRef, cfg SourceConfig) error {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	if err := validateConfig(ref, cfg); err != nil {
		return err
	}
	fp, err := fingerprint(ref, cfg, m.opts.Cwd)
	if err != nil {
		return err
	}
	if existing, getErr := m.get(ref); getErr == nil && existing != nil {
		existing.enabled.Store(false)
		existing.mu.Lock()
		existing.approval = ""
		existing.storedApproval = ""
		existing.mu.Unlock()
		if err := m.Disconnect(ref); err != nil {
			return err
		}
	}
	if cfg.StateDir {
		cfg.stateHome = sourceHome(ref)
	}
	s := &source{ref: ref, cfg: cfg, fingerprint: fp, state: "disconnected", auth: "not-required"}
	s.enabled.Store(false) // configuration is never authority
	if len(cfg.Headers) > 0 || len(cfg.Env) > 0 {
		s.auth = "configured"
	}
	if cfg.Runtime == "cli" {
		if manifest, loadErr := loadManifest(expandPath(cfg.Manifest)); loadErr == nil {
			for _, tool := range manifest.Tools {
				s.catalog.Tools = append(s.catalog.Tools, specOf(tool))
			}
			s.version = manifest.Plugin.Version
			s.state = "on demand"
		}
	}
	m.revision.Add(1)
	m.publish(s)
	m.mu.Lock()
	m.sources[ref.String()] = s
	m.mu.Unlock()
	return m.save()
}

// ReplaceSourceConfig persists and activates one replacement definition. The
// resulting Source is always disabled and unapproved; private state_dir data and
// named credentials are outside the definition and are deliberately untouched.
func (m *Manager) ReplaceSourceConfig(ref SourceRef, cfg SourceConfig) error {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	if err := validateConfig(ref, cfg); err != nil {
		return err
	}
	if err := WriteSource(m.opts.ConfigPath, ref, &cfg); err != nil {
		return err
	}
	return m.ConfigureSource(ref, cfg)
}

// RemoveSource forgets one live source after disconnecting it. Persistent login
// state is intentionally separate: Reset is the explicit destructive action.
func (m *Manager) RemoveSource(ref SourceRef) error {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	if _, err := m.get(ref); err != nil {
		return err
	}
	if err := m.Disconnect(ref); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sources, ref.String())
	m.mu.Unlock()
	m.revision.Add(1)
	return m.forgetPersisted(ref)
}

func (m *Manager) forgetPersisted(ref SourceRef) error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	data := map[string]persisted{}
	if raw, err := readLimited(m.statePath(), 16<<20); err == nil {
		_ = json.Unmarshal(raw, &data)
	} else if !os.IsNotExist(err) {
		return err
	}
	delete(data, ref.String())
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(m.statePath(), raw)
}

// Source returns one safe snapshot, or false when it is not configured.
func (m *Manager) Source(ref SourceRef) (Snapshot, bool) {
	if m == nil {
		return Snapshot{}, false
	}
	for _, snapshot := range m.List() {
		if snapshot.Ref == ref {
			return snapshot, true
		}
	}
	return Snapshot{}, false
}

// EnabledSourceConfig persists only the enabled bit of a source configuration.
// It is used after an explicit GUI/CLI approval, never during discovery.
func (m *Manager) EnabledSourceConfig(ref SourceRef, enabled bool) error {
	cfg, err := m.Config(ref)
	if err != nil {
		return err
	}
	cfg.Enabled = enabled
	if err := WriteSource(m.opts.ConfigPath, ref, &cfg); err != nil {
		return err
	}
	return nil
}

// HasSource reports whether a source identity is currently registered.
func (m *Manager) HasSource(ref SourceRef) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.sources[strings.TrimSpace(ref.String())]
	return ok
}
