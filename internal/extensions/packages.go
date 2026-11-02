package extensions

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// PackageView is a first-party extension package Golder knows how to host but
// never installs, registers, enables or executes by discovery alone.
type PackageView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Source      SourceRef `json:"source"`
	State       string    `json:"state"`
	Executable  string    `json:"executable,omitempty"`
	Configured  bool      `json:"configured"`
	NextAction  string    `json:"next_action"`
	Docs        string    `json:"docs"`
}

// PackageViews returns the small first-party package catalog. These rows make a
// shipped-but-unconfigured package discoverable without turning discovery into
// installation or source authority.
func (m *Manager) PackageViews() []PackageView {
	ref := SourceRef{Kind: "mcp", ID: "lark"}
	p := PackageView{
		ID: "lark", Name: "Lark + Project", Type: "MCP package", Source: ref,
		Description: "Feishu/Lark documents, messages, Sheets, Base, and Project work items through user-authorized native CLIs.",
		State:       "not installed", Docs: "https://github.com/getan/golder/blob/master/docs/lark-cli-plugin.md",
		NextAction: "Install the matching golder-lark release package and the official native CLIs explicitly; nothing is auto-installed.",
	}
	for _, source := range m.List() {
		if source.Ref == ref {
			p.Configured, p.State = true, "configured as mcp:lark"
			p.NextAction = "Open the MCP source above to review, enable, authorize, or diagnose it."
			return []PackageView{p}
		}
	}
	if path := packageExecutable("golder-lark"); path != "" {
		p.State, p.Executable = "installed · not configured", path
		p.NextAction = "Run golder-lark print-config with absolute native CLI paths, merge the disabled mcp:lark table into config.toml, then review and enable it."
	}
	return []PackageView{p}
}

func packageExecutable(name string) string {
	file := name
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), file)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	if path, err := exec.LookPath(file); err == nil {
		if absolute, err := filepath.Abs(path); err == nil {
			return absolute
		}
		return path
	}
	return ""
}
