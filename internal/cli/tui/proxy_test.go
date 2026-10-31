package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
)

func TestProxyPickerToggleAndAddress(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	m := NewModel(Options{})
	got, _ := m.runSlash("/proxy")
	m = got.(Model)
	if m.menu.pickKind != "proxy" {
		t.Fatal("proxy picker did not open")
	}
	view := m.menu.view(120)
	for _, want := range []string{
		"Browser pane (Chromium)",
		"Providers (chat and model discovery)",
		"browser applies on the next launch",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("proxy picker missing %q:\n%s", want, view)
		}
	}
	for i, item := range m.menu.pick {
		if item.Value == "browser on" {
			m.menu.selected = i
			break
		}
	}
	got, _ = m.submitSlashSelected()
	m = got.(Model)
	cfg, err := config.LoadProxyConfig()
	if err != nil || !cfg.Browser {
		t.Fatalf("browser proxy selection not persisted: %v", err)
	}
	if m.menu.pickKind != "proxy" || m.menu.pick[m.menu.selected].Value != "browser off" {
		t.Fatal("browser row should stay selected and switch to off")
	}
	for i, item := range m.menu.pick {
		if item.Value == "openai on" {
			m.menu.selected = i
			break
		}
	}
	got, _ = m.submitSlashSelected()
	m = got.(Model)
	cfg, err = config.LoadProxyConfig()
	if err != nil || !slices.Contains(cfg.Providers, "openai") {
		t.Fatalf("selection not persisted: %v", err)
	}
	if m.menu.pickKind != "proxy" || m.menu.pick[m.menu.selected].Value != "openai off" {
		t.Fatal("picker should stay open on updated selection")
	}
	got, _ = m.submitSlashSelected()
	m = got.(Model)
	cfg, _ = config.LoadProxyConfig()
	if slices.Contains(cfg.Providers, "openai") {
		t.Fatal("toggle off not persisted")
	}
	m.menu.selected = 0
	got, _ = m.submitSlashSelected()
	m = got.(Model)
	if m.menu.picking() || m.input.Value() != "/proxy url " {
		t.Fatal("address action should open editable command")
	}
}

func TestProviderPickerShowsBaseURLAtNormalWidth(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("OPENAI_BASE_URL", "https://relay.example/v1")
	m := NewModel(Options{})
	m.session = &runSession{}
	got, _ := m.runSlash("/provider")
	m = got.(Model)
	for i, item := range m.menu.pick {
		if item.Value == "openai" {
			m.menu.selected = i
			break
		}
	}
	view := m.menu.view(80)
	for _, want := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "https://relay.example/v1", "direct"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker missing %q at 80 columns", want)
		}
	}
	if strings.Count(view, "\n")+1 != m.menu.rows(80) {
		t.Fatal("picker reserved height does not match detail area")
	}
}
