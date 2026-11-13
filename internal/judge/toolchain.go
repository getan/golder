package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/toolchain"
)

type toolchainPlanKey struct{}

func requestedProfile(call agentcore.AgentToolCall) string {
	if !strings.EqualFold(call.Name, "bash") {
		return ""
	}
	var a struct {
		Profile string `json:"profile"`
	}
	_ = json.Unmarshal(call.Arguments, &a)
	return a.Profile
}

func planFromContext(ctx context.Context) *toolchain.Plan {
	p, _ := ctx.Value(toolchainPlanKey{}).(*toolchain.Plan)
	return p
}

func profileCovered(p *toolchain.Plan, cwd string) bool {
	covered := func(path string, write bool) bool {
		cap := grants.Read
		if write {
			cap = grants.Write
		}
		if grants.Check(path, cap).Decision == grants.Denied {
			return false
		}
		return permissions.InsideResolved(cwd, path) || grants.Allows(path, cap) || (!write && permissions.InsideSystemReadRoots(path))
	}
	for _, path := range append(append([]string{}, p.ReadDirs...), p.ReadFiles...) {
		if !covered(path, false) {
			return false
		}
	}
	for _, path := range p.WriteDirs {
		if !covered(path, true) {
			return false
		}
	}
	return true
}

func profileRefused(p *toolchain.Plan) bool {
	for _, path := range append(append([]string{}, p.ReadDirs...), p.ReadFiles...) {
		if grants.Check(path, grants.Read).Decision == grants.Denied {
			return true
		}
	}
	for _, path := range p.WriteDirs {
		if grants.Check(path, grants.Write).Decision == grants.Denied {
			return true
		}
	}
	return false
}

func profileGrant(p *toolchain.Plan) *agentcore.ToolchainGrant {
	return &agentcore.ToolchainGrant{Name: p.Name, Binding: p.Binding, Executable: p.Executable, GoRoot: p.GoRoot, ReadDirs: append([]string{}, p.ReadDirs...), WriteDirs: append([]string{}, p.WriteDirs...), ReadFiles: append([]string{}, p.ReadFiles...)}
}

func applyProfile(p *toolchain.Plan, scope Scope) (*agentcore.ToolchainGrant, error) {
	if p == nil || profileRefused(p) {
		return nil, errors.New("toolchain profile is refused")
	}
	if scope != ScopeOnce {
		if grants.Process() == nil {
			return nil, errors.New("permission runtime is not initialized")
		}
		life := grants.Session
		if scope == ScopeRemembered {
			life = grants.Remembered
		}
		for _, path := range p.ReadDirs {
			if _, err := grants.AddReadRoot(path, life, "toolchain:"+p.Name); err != nil {
				return nil, err
			}
		}
		for _, path := range p.ReadFiles {
			if _, err := grants.Add(grants.Grant{Path: path, Allow: grants.Read, Scope: grants.Exact, Lifetime: life, Source: "toolchain:" + p.Name}); err != nil {
				return nil, err
			}
		}
		for _, path := range p.WriteDirs {
			if _, err := grants.AddWritableRoot(path, life, "toolchain:"+p.Name); err != nil {
				return nil, err
			}
		}
	}
	return profileGrant(p), nil
}

func profileLabel(p *toolchain.Plan) string {
	if p == nil {
		return "toolchain (unresolved)"
	}
	return fmt.Sprintf("toolchain %s (%s; selected first on PATH): read dirs %s; write dirs %s; read files %s", p.Name, p.Executable, strings.Join(p.ReadDirs, ", "), strings.Join(p.WriteDirs, ", "), strings.Join(p.ReadFiles, ", "))
}

func profileBlocked(call agentcore.AgentToolCall, mode permissions.Mode, reason, message string) *agentcore.BeforeToolCallDecision {
	dec := blockCall(call, Verdict{Level: Deny, Source: "toolchain"}, message)
	dec.Authorization = &agentcore.AuthorizationDecision{Mode: mode.String(), Source: "toolchain", Reason: reason, Rationale: message}
	return dec
}
