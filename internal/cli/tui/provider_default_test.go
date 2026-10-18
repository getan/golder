package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/cli/config"
)

// The picker marks the provider new sessions start on, keeps that tag separate
// from "current", and sets it on `d` without leaving the list.
func TestProviderPickerMarksAndSetsTheDefault(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if _, err := config.SetDefaultProvider("deepseek", ""); err != nil {
		t.Fatal(err)
	}
	m := NewModel(Options{})
	m.session = &runSession{}
	m.live.Model = "muse-spark-1.3-contributor"
	m.live.ProviderName = "opencode-go"

	got, _ := m.runSlash("/provider")
	m = got.(Model)
	if !m.menu.picking() || m.menu.pickKind != "provider" {
		t.Fatalf("picker not open: picking=%v kind=%q", m.menu.picking(), m.menu.pickKind)
	}
	var marked []string
	for _, it := range m.menu.pick {
		if it.Default {
			marked = append(marked, it.Value)
		}
		if it.Value == "opencode-go" && it.Default {
			t.Error("the live provider must not be tagged default on its own")
		}
	}
	if len(marked) != 1 || marked[0] != "deepseek" {
		t.Fatalf("default tags = %v, want [deepseek]", marked)
	}
	if marked[0] == m.live.ProviderName {
		t.Error("the default and the current provider should be distinguishable here")
	}

	// `d` on the highlighted row writes config.toml, reports, and reopens the
	// list with the highlight where it was.
	for i, it := range m.menu.pick {
		if it.Value == "commandcode" {
			m.menu.selected = i
		}
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = next.(Model)
	if name, err := config.DefaultProvider(); err != nil || name != "commandcode" {
		t.Fatalf("DefaultProvider after d = %q, %v", name, err)
	}
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "default provider set to commandcode") {
		t.Errorf("transcript should report the new default:\n%s", joined)
	}
	if !m.menu.picking() {
		t.Fatal("the picker should stay open so the tag is visible in place")
	}
	if sel, ok := m.menu.pickCurrent(); !ok || sel != "commandcode" {
		t.Errorf("highlight = %q,%v, want it kept on commandcode", sel, ok)
	}
	banner := m.menu.view(120)
	if !strings.Contains(stripANSI(banner), "(default)") {
		t.Errorf("picker rows should carry the default tag:\n%s", stripANSI(banner))
	}
}
