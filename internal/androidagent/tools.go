// Package androidagent assembles the experimental in-process Android tool set.
// It is separate from desktop tool wiring: constructing it requires an Android
// host to provide the actual per-call authorization gate.
package androidagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/execsess"
	"github.com/getan/golder/internal/permissions"
)

// Config is supplied by the Android host, not by the model. BeforeToolCall must
// be the host's actual authorization gate; the tool set never manufactures an
// unconfined-execution grant. A nil Permissions selects auto mode.
type Config struct {
	Root           string
	Permissions    *permissions.State
	BeforeToolCall agentcore.BeforeToolCallFunc
}

// Capabilities describe executable paths found at construction, not a promise
// that a subsequent call will be authorized or that execution will succeed.
type Capabilities struct {
	Ripgrep string
	Shell   string
	Grep    string
	Find    string
}

// Toolset carries one matched catalog, prompt and execution gate. A caller must
// pass Batch to the agent loop and Tools to the model context together.
type Toolset struct {
	Tools        []agentcore.AgentTool
	Batch        agenttool.BatchConfig
	Permissions  *permissions.State
	Capabilities Capabilities
	Root         string
	shell        *agenttool.BashTool
}

// New builds the Android catalog using the current app process's executable
// paths. It refuses other platforms rather than changing their existing wiring.
func New(cfg Config) (*Toolset, error) {
	if runtime.GOOS != "android" {
		return nil, fmt.Errorf("androidagent: Android host required (got %s)", runtime.GOOS)
	}
	return newToolset(cfg, exec.LookPath)
}

func newToolset(cfg Config, lookPath func(string) (string, error)) (*Toolset, error) {
	if cfg.BeforeToolCall == nil {
		return nil, fmt.Errorf("androidagent: host authorization gate is required")
	}
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, fmt.Errorf("androidagent: workspace root is required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("androidagent: workspace: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("androidagent: workspace: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("androidagent: workspace is not a readable directory")
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	find := func(name string) string {
		path, err := lookPath(name)
		if err != nil {
			return ""
		}
		return path
	}
	caps := Capabilities{
		Ripgrep: find("rg"),
		Shell:   find("/system/bin/sh"),
		Grep:    find("/system/bin/grep"),
		Find:    find("/system/bin/find"),
	}
	mode := cfg.Permissions
	if mode == nil {
		mode = permissions.New(permissions.Auto)
	}
	s := &Toolset{Root: root, Capabilities: caps, Permissions: mode}
	s.Tools = []agentcore.AgentTool{
		&agenttool.ReadTool{Root: root},
		&agenttool.LsTool{Root: root},
		&agenttool.ApplyPatchTool{Root: root, Snap: agenttool.NewFileSnapshotRecorder()},
	}
	if caps.Ripgrep != "" {
		s.Tools = append(s.Tools, &agenttool.GrepTool{Root: root}, &agenttool.FindTool{Root: root})
	}
	if caps.Shell != "" {
		sessions := execsess.NewManager()
		s.shell = &agenttool.BashTool{
			Dir: root, Shell: caps.Shell, Sessions: sessions, Permissions: mode,
			// Android App isolation is not a workspace sandbox. Without a host
			// gate's per-call bare/direct grant, shell execution must fail closed.
			RequireSandbox: true,
		}
		s.Tools = append(s.Tools, &shellTool{tool: s.shell}, &agenttool.WriteStdinTool{Sessions: sessions})
	}
	registry := agenttool.NewToolRegistry()
	for _, tool := range s.Tools {
		if err := registry.Register(tool); err != nil {
			return nil, fmt.Errorf("androidagent: register %s: %w", tool.Name(), err)
		}
	}
	s.Batch = agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{
		Registry: registry, BeforeToolCall: cfg.BeforeToolCall,
	}}
	return s, nil
}

// Prompt is intentionally Android-specific: the desktop prompt's OS-sandbox
// claims are not true of a shell running under only the Android App sandbox.
// This is an embedding prototype; host-provided instructions may be appended.
func (s *Toolset) Prompt() string {
	var b strings.Builder
	b.WriteString("You are golder, a coding agent running inside an Android app. Work within the user's selected workspace. ")
	b.WriteString("Use read, ls and apply_patch for workspace files. Preserve task intent, report tool failures and do not replay a write whose outcome is unknown. ")
	b.WriteString("Shell commands are subject to the host authorization gate. Android app isolation is not a workspace-level shell sandbox: a shell may reach other data the app can access, and per-command network isolation is not provided here. ")
	switch {
	case s.Capabilities.Ripgrep != "":
		b.WriteString("The grep and find tools are available through ripgrep; use them for code search.")
	case s.Capabilities.Shell == "":
		b.WriteString("Ripgrep and the shell are unavailable; grep and find tools are not exposed. Do not claim code search is available.")
	default:
		b.WriteString("Ripgrep is unavailable and the dedicated grep and find tools are not exposed. ")
		if s.Capabilities.Grep != "" {
			fmt.Fprintf(&b, "When the host authorizes a shell call, use %s for content search. ", s.Capabilities.Grep)
		} else {
			b.WriteString("System grep was not found. ")
		}
		if s.Capabilities.Find != "" {
			fmt.Fprintf(&b, "When the host authorizes a shell call, use %s for filename search. ", s.Capabilities.Find)
		} else {
			b.WriteString("System find was not found. ")
		}
		b.WriteString("System command syntax and ignore behavior can differ from ripgrep. If the active permissions do not allow shell execution, report search unavailable rather than claiming it succeeded.")
	}
	return b.String()
}

// Close stops commands launched by this tool set when the host ends the run.
func (s *Toolset) Close() {
	if s != nil && s.shell != nil {
		s.shell.StopSessions()
	}
}
