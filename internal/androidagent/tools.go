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
	"github.com/getan/golder/internal/androiduse"
	"github.com/getan/golder/internal/execsess"
	"github.com/getan/golder/internal/mobilebuild"
	"github.com/getan/golder/internal/mobilegit"
	"github.com/getan/golder/internal/permissions"
)

// Config is supplied by the Android host, not by the model. BeforeToolCall must
// be the host's actual authorization gate; the tool set never manufactures an
// unconfined-execution grant. A nil Permissions selects auto mode.
type Config struct {
	Root           string
	Permissions    *permissions.State
	BeforeToolCall agentcore.BeforeToolCallFunc
	// NoShell omits bash/write_stdin even when /system/bin/sh exists. A host
	// that has not built a command sandbox yet can still offer the file tools;
	// the prompt then says shell is unavailable instead of advertising a tool
	// the catalog does not contain.
	NoShell bool
	// NoGit omits the structured Git tool. The tool is local by default and
	// gates clone/fetch/push on the host's per-call network grant.
	NoGit          bool
	GitAuthorName  string
	GitAuthorEmail string
	GitToken       string
	// AndroidUseEndpoint is the app-private endpoint file exposed by the
	// Kotlin AccessibilityService host. Empty means AndroidUse is not authorized.
	AndroidUseEndpoint string
	// AndroidUsePhase2 enables the separate high-privilege background-display
	// surface. It must be explicitly consented to by the user.
	AndroidUsePhase2 bool
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
	Tools            []agentcore.AgentTool
	Batch            agenttool.BatchConfig
	Permissions      *permissions.State
	Capabilities     Capabilities
	Root             string
	shell            *agenttool.BashTool
	androidUse       bool
	androidUsePhase2 bool
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
	if cfg.NoShell {
		caps.Shell = ""
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
		// The todo tool is in-memory and keeps the mobile task card useful
		// across a multi-step run. It does not touch the workspace or network.
		&agenttool.TodoTool{Store: agenttool.NewTodoStore()},
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
	if !cfg.NoGit {
		gitTool, err := mobilegit.New(mobilegit.Config{
			Root: root, AuthorName: cfg.GitAuthorName, AuthorEmail: cfg.GitAuthorEmail, Token: cfg.GitToken,
		})
		if err != nil {
			return nil, fmt.Errorf("androidagent: git: %w", err)
		}
		s.Tools = append(s.Tools, gitTool)
	}
	buildTool, err := mobilebuild.New(mobilebuild.Config{
		Root: root, Token: cfg.GitToken,
	})
	if err != nil {
		return nil, fmt.Errorf("androidagent: build: %w", err)
	}
	downloadTool, err := mobilebuild.NewDownload(mobilebuild.Config{
		Root: root, Token: cfg.GitToken,
	})
	if err != nil {
		return nil, fmt.Errorf("androidagent: download: %w", err)
	}
	installTool, err := mobilebuild.NewInstall(mobilebuild.Config{Root: root})
	if err != nil {
		return nil, fmt.Errorf("androidagent: install: %w", err)
	}
	s.Tools = append(s.Tools, buildTool, downloadTool, installTool)
	if strings.TrimSpace(cfg.AndroidUseEndpoint) != "" {
		s.Tools = append(s.Tools, androiduse.NewToolWithPhase2(cfg.AndroidUseEndpoint, cfg.AndroidUsePhase2))
	}
	s.androidUse = strings.TrimSpace(cfg.AndroidUseEndpoint) != ""
	s.androidUsePhase2 = s.androidUse && cfg.AndroidUsePhase2
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
	b.WriteString("For multi-step work, use the todo tool to keep a short task list and update it as work progresses. ")
	b.WriteString("Shell commands are subject to the host authorization gate. Android app isolation is not a workspace-level shell sandbox: a shell may reach other data the app can access, and per-command network isolation is not provided here. ")
	b.WriteString("A structured git tool is available for local status/log/diff/add/commit/branch/checkout/remote and for clone/fetch/push when the host grants network access for that exact call. ")
	b.WriteString("When a tool result contains APK_PATH or INSTALL_APK, the host transcript shows an Install APK button. If an APK is already available, first tell the user to tap that button; do not rebuild just to install. ")
	b.WriteString("Only if the user explicitly asks you to trigger installation yourself, call android_install with the APK path; the host will open the package installer. Never claim the app is installed until the user confirms the system dialog. ")
	b.WriteString("Use android_download to fetch the latest successful artifact without rebuilding. Use android_build only when code changed or no APK exists; it reuses a successful run for the current commit unless force=true. ")
	if s.androidUse {
		b.WriteString("AndroidUse is available through android_use: call observe first, use node refs with the current snapshotId, and prefer semantic refs over coordinates. It operates the visible phone screen; never change Golder's own permissions or settings. The current tool catalog is authoritative; do not infer that tools are unavailable from earlier assistant replies. ")
	}
	if s.androidUsePhase2 {
		b.WriteString("Background-display control is separately enabled through android_use: use background_status, background_start, background_open_app, background_observe and background_tap. Prefer it whenever you operate another app: it leaves the person's screen free, which is the normal expectation for a phone task. While background control is available the host refuses a visible-screen open_app unless you pass foreground=true, and that flag is for the case where the person explicitly asks to watch or take part; say which path you took and why. After a background refusal, ask the person to close the app from recents and retry background_open_app. After user handback, observe and pass the new snapshotId before acting. Release and stop when appropriate, respecting the user's request to keep a display open. It runs through the user-consented shell helper and must not be treated as ordinary app permission. ")
	}
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
