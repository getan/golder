package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
)

func TestProxyPickerToggleAndAddress(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	m := NewModel(Options{})
	got, _ := m.runSlash("/proxy")
	m = got.(Model)
	if m.menu.pickKind != "proxy" {
		t.Fatal("proxy picker did not open")
	}
	for i, item := range m.menu.pick {
		if item.Value == "openai on" {
			m.menu.selected = i
			break
		}
	}
	got, _ = m.submitSlashSelected()
	m = got.(Model)
	cfg, err := config.LoadProxyConfig()
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	if strings.Count(view, "\n")+1 != m.menu.rows() {
		t.Fatal("picker reserved height does not match detail area")
	}
}
