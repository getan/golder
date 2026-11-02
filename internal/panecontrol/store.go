// Package panecontrol owns the cross-process registry of top-level Golder
// agents and the exclusive write lease for panes in one tuios daemon.
package panecontrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion = 1
	ScopeOnce     = "one-call"
	ScopeSession  = "session"
	heartbeatTTL  = 90 * time.Second
)

type Agent struct {
	ID             string    `json:"agent_id"`
	Label          string    `json:"label"`
	Project        string    `json:"project"`
	Role           string    `json:"role"`
	ConversationID string    `json:"conversation_id"`
	SessionID      string    `json:"session_id"`
	PaneID         string    `json:"pane_id"`
	Workspace      int       `json:"workspace"`
	Mode           string    `json:"mode"`
	State          string    `json:"state,omitempty"`
	PID            int       `json:"pid"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Lease struct {
	SessionID      string    `json:"session_id"`
	WindowID       string    `json:"window_id"`
	Workspace      int       `json:"workspace"`
	AgentID        string    `json:"agent_id"`
	AgentLabel     string    `json:"agent_label"`
	ConversationID string    `json:"conversation_id"`
	Scope          string    `json:"scope"`
	Uses           int       `json:"uses,omitempty"`
	DaemonID       string    `json:"daemon_id"`
	HostHint       string    `json:"host_hint,omitempty"`
	Foreground     string    `json:"foreground,omitempty"`
	Remote         bool      `json:"remote,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type ClaimRequest struct {
	Lease
	Transfer bool
}

type TransferRequiredError struct{ Current Lease }

func (e *TransferRequiredError) Error() string {
	return fmt.Sprintf("pane is controlled by %s; set transfer=true and obtain explicit operator confirmation to transfer it", e.Current.AgentLabel)
}

type NotControllerError struct{ Current *Lease }

func (e *NotControllerError) Error() string {
	if e.Current == nil {
		return "pane has no write-control lease for this agent"
	}
	return fmt.Sprintf("pane write control belongs to %s", e.Current.AgentLabel)
}

type state struct {
	Version int              `json:"version"`
	Agents  map[string]Agent `json:"agents"`
	Leases  map[string]Lease `json:"leases"`
}

type Store struct {
	path string
	now  func() time.Time
}

func New(socket string) *Store {
	return &Store{path: filepath.Clean(socket) + ".golder-pane-control.json", now: time.Now}
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func leaseKey(session, window string) string { return session + "\x00" + window }

func (s *Store) load() (state, error) {
	out := state{Version: SchemaVersion, Agents: map[string]Agent{}, Leases: map[string]Lease{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return state{}, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return state{}, fmt.Errorf("pane control: decode state: %w", err)
	}
	if out.Version != SchemaVersion {
		return state{}, fmt.Errorf("pane control: state version %d is unsupported", out.Version)
	}
	if out.Agents == nil {
		out.Agents = map[string]Agent{}
	}
	if out.Leases == nil {
		out.Leases = map[string]Lease{}
	}
	return out, nil
}

func (s *Store) save(st state) error {
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".golder-pane-control-*")
	if err != nil {
		return err
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	defer cleanup()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func (s *Store) withState(write bool, fn func(*state) error) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return errors.New("pane control store is not configured")
	}
	unlock, err := acquireLock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	st, err := s.load()
	if err != nil {
		return err
	}
	changed := prune(&st, s.now())
	if err := fn(&st); err != nil {
		return err
	}
	if write || changed {
		return s.save(st)
	}
	return nil
}

func prune(st *state, now time.Time) bool {
	changed := false
	cut := now.Add(-heartbeatTTL)
	for id, agent := range st.Agents {
		if agent.UpdatedAt.Before(cut) {
			delete(st.Agents, id)
			changed = true
		}
	}
	for key, lease := range st.Leases {
		agent, ok := st.Agents[lease.AgentID]
		if !ok || agent.ConversationID != lease.ConversationID || agent.SessionID != lease.SessionID || agent.Workspace != lease.Workspace {
			delete(st.Leases, key)
			changed = true
		}
	}
	return changed
}

func (s *Store) Register(agent Agent) error {
	if agent.ID == "" || agent.SessionID == "" || agent.PaneID == "" || agent.ConversationID == "" {
		return errors.New("pane control: incomplete agent identity")
	}
	return s.withState(true, func(st *state) error {
		agent.UpdatedAt = s.now().UTC()
		st.Agents[agent.ID] = agent
		for key, lease := range st.Leases {
			if lease.AgentID == agent.ID && (lease.ConversationID != agent.ConversationID || lease.SessionID != agent.SessionID || lease.Workspace != agent.Workspace) {
				delete(st.Leases, key)
			}
		}
		return nil
	})
}

func (s *Store) Unregister(agentID string) error {
	if agentID == "" {
		return nil
	}
	return s.withState(true, func(st *state) error {
		delete(st.Agents, agentID)
		for key, lease := range st.Leases {
			if lease.AgentID == agentID {
				delete(st.Leases, key)
			}
		}
		return nil
	})
}

func (s *Store) Agents(session string, workspace int) ([]Agent, error) {
	var out []Agent
	err := s.withState(false, func(st *state) error {
		for _, agent := range st.Agents {
			blocked := agent.State == "needs_input" || agent.State == "needs-input"
			if agent.SessionID == session && agent.Workspace == workspace && agent.Mode != "read-only" && !blocked {
				out = append(out, agent)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label == out[j].Label {
			return out[i].ID < out[j].ID
		}
		return out[i].Label < out[j].Label
	})
	return out, err
}

func (s *Store) Claim(req ClaimRequest) (Lease, error) {
	lease := req.Lease
	if lease.SessionID == "" || lease.WindowID == "" || lease.AgentID == "" || lease.ConversationID == "" || lease.Workspace <= 0 {
		return Lease{}, errors.New("pane control: incomplete lease identity")
	}
	if lease.Scope != ScopeOnce && lease.Scope != ScopeSession {
		return Lease{}, errors.New("pane control: scope must be one-call or session")
	}
	if lease.Scope == ScopeOnce {
		lease.Uses = 1
	} else {
		lease.Uses = 0
	}
	err := s.withState(true, func(st *state) error {
		agent, ok := st.Agents[lease.AgentID]
		if !ok || agent.ConversationID != lease.ConversationID || agent.SessionID != lease.SessionID || agent.Workspace != lease.Workspace {
			return errors.New("pane control: target agent is not an eligible online agent in this workspace")
		}
		key := leaseKey(lease.SessionID, lease.WindowID)
		if current, ok := st.Leases[key]; ok && current.AgentID != lease.AgentID && !req.Transfer {
			return &TransferRequiredError{Current: current}
		}
		lease.AgentLabel = agent.Label
		lease.CreatedAt = s.now().UTC()
		st.Leases[key] = lease
		return nil
	})
	return lease, err
}

func (s *Store) Get(session, window string) (Lease, bool, error) {
	var lease Lease
	var ok bool
	err := s.withState(false, func(st *state) error {
		lease, ok = st.Leases[leaseKey(session, window)]
		return nil
	})
	return lease, ok, err
}

func (s *Store) Authorize(session, window, agentID, conversationID string, consume bool) (Lease, error) {
	var out Lease
	err := s.withState(consume, func(st *state) error {
		key := leaseKey(session, window)
		lease, ok := st.Leases[key]
		if !ok {
			return &NotControllerError{}
		}
		if lease.AgentID != agentID || lease.ConversationID != conversationID {
			copy := lease
			return &NotControllerError{Current: &copy}
		}
		out = lease
		if consume && lease.Scope == ScopeOnce {
			delete(st.Leases, key)
		}
		return nil
	})
	return out, err
}

func (s *Store) Revoke(session, window, agentID string, operator bool) (bool, error) {
	revoked := false
	err := s.withState(true, func(st *state) error {
		key := leaseKey(session, window)
		lease, ok := st.Leases[key]
		if !ok {
			return nil
		}
		if !operator && lease.AgentID != agentID {
			copy := lease
			return &NotControllerError{Current: &copy}
		}
		delete(st.Leases, key)
		revoked = true
		return nil
	})
	return revoked, err
}

func (s *Store) PruneWindows(session string, live map[string]bool) error {
	return s.withState(true, func(st *state) error {
		for key, lease := range st.Leases {
			if lease.SessionID == session && !live[lease.WindowID] {
				delete(st.Leases, key)
			}
		}
		return nil
	})
}
