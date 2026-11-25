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
	Version     string    `json:"version,omitempty"`
	Configured  bool      `json:"configured"`
	NextAction  string    `json:"next_action"`
	Docs        string    `json:"docs"`
}

// PackageViews returns the small first-party package catalog. These rows make a
// shipped-but-unconfigured package discoverable without turning discovery into
// installation or source authority.
func (m *Manager) PackageViews() []PackageView {
	larkRef := SourceRef{Kind: "mcp", ID: "lark"}
	lark := PackageView{
		ID: "lark", Name: "Lark + Project", Type: "MCP package", Source: larkRef,
		Description: "Feishu/Lark documents, messages, Sheets, Base, and Project work items through user-authorized native CLIs.",
		State:       "not installed", Docs: "https://github.com/getan/golder/blob/master/docs/lark-cli-plugin.md",
		NextAction: "Install the matching golder-lark release package and the official native CLIs explicitly; nothing is auto-installed.",
	}
	if installed, ok := InstalledManagedPackage("lark"); ok {
		lark.State, lark.Executable, lark.Version = "installed · not configured", installed.Executable, installed.Version
		lark.NextAction = "Configure the official native CLIs and application, then explicitly approve and authorize the package."
	}
	for _, source := range m.List() {
		if source.Ref == larkRef {
			lark.Configured, lark.State = true, source.State
			if source.Version != "" {
				lark.Version = source.Version
			}
			lark.NextAction = "Open the MCP source above to review, enable, authorize, or diagnose it."
			break
		}
	}
	if lark.Executable == "" {
		if path := packageExecutable("golder-lark"); path != "" {
			lark.State, lark.Executable = "installed · not configured", path
			lark.NextAction = "Run golder-lark print-config with absolute native CLI paths, merge the disabled mcp:lark table into config.toml, then review and enable it."
		}
	}

	docxRef := SourceRef{Kind: "mcp", ID: "docx"}
	docx := PackageView{
		ID: "docx", Name: "DOCX", Type: "MCP package", Source: docxRef,
		Description: "Read, create and edit Word .docx files with headings, tables and images, confined to the workspace.",
		State:       "not installed", Docs: "https://github.com/getan/golder/blob/master/extension/plugins/docx/README.md",
		NextAction: "Install the matching golder-docx release package, then approve and enable it; no application credentials are required.",
	}
	if installed, ok := InstalledManagedPackage("docx"); ok {
		docx.State, docx.Executable, docx.Version = "installed · not configured", installed.Executable, installed.Version
		docx.NextAction = "Approve and enable the workspace-confined DOCX MCP source."
	}
	for _, source := range m.List() {
		if source.Ref == docxRef {
			docx.Configured, docx.State = true, source.State
			if source.Version != "" {
				docx.Version = source.Version
			}
			docx.NextAction = "Open the MCP source above to review, enable, or diagnose it."
			break
		}
	}
	if docx.Executable == "" {
		if path := packageExecutable("golder-docx"); path != "" {
			docx.State, docx.Executable = "installed · not configured", path
			docx.NextAction = "Merge the disabled mcp:docx source from the plugin README into config.toml, then review and enable it."
		}
	}
	return []PackageView{lark, docx}
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
