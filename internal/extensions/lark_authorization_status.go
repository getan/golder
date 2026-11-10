package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/getan/golder/extension"
)

// LarkServiceAuthorization describes only a bounded status probe. Unknown is
// deliberately distinct from requiring login: network and CLI failures must not
// trigger a new authorization flow.
type LarkServiceAuthorization struct {
	State string // authorized, required, unavailable, or unknown
}

type LarkAuthorizationStatus struct {
	Lark, Project LarkServiceAuthorization
}

// ProbeLarkAuthorization is an operator-initiated, fixed read-only status call.
// It cannot invoke arbitrary tools or authorize writes and retains the normal
// source approval, sandbox, tool policy, lifetime and fingerprint checks.
func (m *Manager) ProbeLarkAuthorization(ctx context.Context) (LarkAuthorizationStatus, error) {
	if err := ctx.Err(); err != nil {
		return LarkAuthorizationStatus{}, err
	}
	ref := SourceRef{Kind: "mcp", ID: "lark"}
	s, err := m.get(ref)
	if err != nil {
		return LarkAuthorizationStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Transport != "stdio" || filepath.Base(s.cfg.Command) != executableName("golder-lark") {
		return LarkAuthorizationStatus{}, errors.New("Lark status requires the reviewed managed helper")
	}
	if _, managed := ManagedPackageForExecutable("lark", s.cfg.Command); !managed {
		return LarkAuthorizationStatus{}, errors.New("Lark status requires the verified managed helper")
	}
	if err := m.connectLocked(ctx, s); err != nil {
		return LarkAuthorizationStatus{}, err
	}
	if !m.toolAllowed(s, "status") {
		return LarkAuthorizationStatus{}, errors.New("Lark status is blocked by extension policy")
	}
	var spec *extension.ToolSpec
	for i := range s.catalog.Tools {
		if s.catalog.Tools[i].Name == "status" {
			spec = &s.catalog.Tools[i]
			break
		}
	}
	if spec == nil {
		return LarkAuthorizationStatus{}, errors.New("Lark helper does not support status checks")
	}
	t := &tool{manager: m, ref: ref, origin: s, spec: *spec}
	if operator, policy := t.approvalFor(s); operator || policy {
		return LarkAuthorizationStatus{}, errors.New("Lark status requires explicit approval")
	}
	if err := ctx.Err(); err != nil {
		return LarkAuthorizationStatus{}, err
	}
	if !s.enabled.Load() || m.closed.Load() {
		return LarkAuthorizationStatus{}, errors.New("extension disabled; status not checked")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.life, cancel)
	defer stop()
	result, err := s.conn.Call(ctx, extension.Call{ID: "gui-lark-status", Tool: "status", Arguments: json.RawMessage(`{"verify":true}`)}, nil)
	if err != nil || result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
		return LarkAuthorizationStatus{}, errors.New("Lark status check failed; authorization is unknown")
	}
	return parseLarkAuthorizationStatus([]byte(result.Content[0].Text))
}

func parseLarkAuthorizationStatus(data []byte) (LarkAuthorizationStatus, error) {
	var payload struct {
		OK   bool `json:"ok"`
		Data struct {
			Services map[string]struct {
				Installed     *bool  `json:"installed"`
				Authenticated *bool  `json:"authenticated"`
				Error         string `json:"error"`
				Details       struct {
					Identity      string `json:"identity"`
					Verified      *bool  `json:"verified"`
					Authenticated *bool  `json:"authenticated"`
				} `json:"details"`
			} `json:"services"`
		} `json:"data"`
	}
	if len(data) > 64<<10 || json.Unmarshal(data, &payload) != nil || !payload.OK || payload.Data.Services == nil {
		return LarkAuthorizationStatus{}, errors.New("invalid Lark status response")
	}
	state := func(name string) LarkServiceAuthorization {
		entry, ok := payload.Data.Services[name]
		if !ok || entry.Installed == nil || entry.Error != "" {
			return LarkServiceAuthorization{State: "unknown"}
		}
		if !*entry.Installed {
			return LarkServiceAuthorization{State: "unavailable"}
		}
		if entry.Authenticated != nil && *entry.Authenticated {
			if name == "project" || (entry.Details.Identity == "user" && entry.Details.Verified != nil && *entry.Details.Verified) {
				return LarkServiceAuthorization{State: "authorized"}
			}
		}
		if name == "project" && entry.Details.Authenticated != nil && !*entry.Details.Authenticated {
			return LarkServiceAuthorization{State: "required"}
		}
		if name == "lark" && entry.Authenticated != nil && !*entry.Authenticated &&
			(entry.Details.Identity == "bot" || (entry.Details.Identity == "user" && entry.Details.Verified != nil && !*entry.Details.Verified)) {
			return LarkServiceAuthorization{State: "required"}
		}
		return LarkServiceAuthorization{State: "unknown"}
	}
	return LarkAuthorizationStatus{Lark: state("lark"), Project: state("project")}, nil
}
