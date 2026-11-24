package extensions

import (
	"errors"
	"strings"

	larkversion "github.com/getan/golder/extension/plugins/lark/version"
)

// LarkSourceRef is the fixed first-party Lark source identity shared by the GUI,
// the TUI and the CLI extension commands.
var LarkSourceRef = SourceRef{Kind: "mcp", ID: "lark"}

// LarkHelperVersion is the helper version this build expects.
func LarkHelperVersion() string { return larkversion.Version }

// LarkActiveHelperVersion reports the managed helper version the configured
// Lark source runs. ok is false when Lark is unconfigured or its command is not
// a managed package.
func LarkActiveHelperVersion(m *Manager) (string, bool) {
	if m == nil {
		return "", false
	}
	cfg, err := m.Config(LarkSourceRef)
	if err != nil {
		return "", false
	}
	pkg, ok := ManagedPackageForExecutable("lark", cfg.Command)
	if !ok {
		return "", false
	}
	return pkg.Version, true
}

// LarkSourceConfigFromExisting rebuilds a Lark declaration around a newly
// installed helper. Every non-secret setting, the named App Secret credential
// reference and the source-scoped login state are preserved.
func LarkSourceConfigFromExisting(pkg ManagedPackage, current SourceConfig) (SourceConfig, error) {
	credential := credentialReferenceName(current.Env["LARK_MCP_APP_SECRET"])
	return LarkSourceConfig(LarkSourceOptions{
		Package: pkg, LarkCLI: current.Env["LARK_MCP_CLI"], ProjectCLI: current.Env["LARK_MCP_PROJECT_CLI"],
		AppID: current.Env["LARK_MCP_APP_ID"], AppSecretCredential: credential,
		Domains: splitCSVList(current.Env["LARK_MCP_DOMAINS"]), ProjectHost: current.Env["MEEGLE_HOST"],
	})
}

// RebindLarkSource points the configured Lark source at an installed helper.
// ReplaceSourceConfig persists the replacement disabled and unapproved, so the
// new helper bytes are reviewed before they can run; credentials and login
// state survive the replacement.
func RebindLarkSource(m *Manager, pkg ManagedPackage) error {
	if m == nil {
		return errors.New("extension manager unavailable")
	}
	current, err := m.Config(LarkSourceRef)
	if err != nil {
		return errors.New("configure Lark before updating its helper")
	}
	updated, err := LarkSourceConfigFromExisting(pkg, current)
	if err != nil {
		return err
	}
	return m.ReplaceSourceConfig(LarkSourceRef, updated)
}

func credentialReferenceName(value string) string {
	const prefix = "${credential:"
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, prefix) && strings.HasSuffix(value, "}") {
		return strings.TrimSuffix(strings.TrimPrefix(value, prefix), "}")
	}
	return ""
}

func splitCSVList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
