package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	larkversion "github.com/getan/golder/extension/plugins/lark/version"
	"github.com/getan/golder/internal/extensions"
)

func TestLarkWizardReusesHelperAfterHostUpgrade(t *testing.T) {
	m := extensionModel(t)
	m.opts.Version = "v9.9.9"
	const content = "fixture-helper"
	path := filepath.Join(t.TempDir(), "golder-lark")
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	pkg, err := extensions.InstallManagedPackage(extensions.PackageInstallOptions{ID: "lark", Version: larkversion.Version, Source: path, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	next, _ := m.beginLarkWizard()
	w := next.(Model).larkWizard
	if w == nil || w.pkg.Executable != pkg.Executable || w.phase == "helper" {
		t.Fatalf("host upgrade requested an unchanged helper: %+v", w)
	}
}

func TestLarkWizardDefaultsAndPrivateSecret(t *testing.T) {
	m := extensionModel(t)
	const content = "fixture-helper"
	path := filepath.Join(t.TempDir(), "golder-lark")
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	pkg, err := extensions.InstallManagedPackage(extensions.PackageInstallOptions{ID: "lark", Version: "1.2.3", Source: path, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in := textinput.New()
	in.Focus()
	in.SetValue("cli_test")
	m.larkWizard = &larkSetupWizard{phase: "app-id", input: in, pkg: pkg, larkCLI: cli, domains: []bool{true, true, true, true, true, true, true, true}}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.larkWizard.phase != "app-secret" || m.larkWizard.input.EchoMode != textinput.EchoPassword {
		t.Fatal("Lark secret field was not masked")
	}
	secret := "fixture-" + "private-value"
	m.larkWizard.input.SetValue(secret)
	if strings.Contains(m.View().Content, secret) {
		t.Fatal("secret was rendered")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.larkWizard.phase != "domains" || len(m.larkWizard.selectedDomains()) != len(larkWizardDomains) {
		t.Fatal("all Lark domains should start selected")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // domains
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // Project remains optional
	if m.larkWizard.phase != "review" || strings.Contains(m.View().Content, secret) {
		t.Fatal("review leaked secret or did not advance")
	}
	next, cmd := m.handleLarkWizardKey(runeKey('s'))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("saving did not schedule an operation")
	}
	m = apply(t, m, cmd())
	if m.larkWizard.phase != "done" || m.larkWizard.err != "" || m.larkWizard.secret != "" {
		t.Fatalf("Lark save failed or retained secret: %s", m.larkWizard.err)
	}
	if strings.Contains(strings.Join(m.transcript.contentLines(), "\n"), secret) {
		t.Fatal("Lark secret leaked into the conversation")
	}
}
