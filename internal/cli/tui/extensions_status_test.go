package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/extensions"
)

// The status row answers "can this be used now". A failed attempt belongs in
// its own history line and must not repaint the status or replace its text.
func TestExtensionRowShowsCurrentStateAndHistorySeparately(t *testing.T) {
	at := time.Date(2026, 11, 18, 14, 32, 0, 0, time.Local)
	row := extensionSourceRow(extensions.Snapshot{
		Ref: extensions.SourceRef{Kind: "mcp", ID: "lark"}, Type: "MCP · stdio",
		State: "on demand", Trust: "approved", Busy: true,
		LastError: "boom", LastErrorScope: "call", LastErrorAt: &at,
	})
	if row.Status != "on demand · in use now" {
		t.Errorf("status = %q, want the current readiness plus the live lock", row.Status)
	}
	if row.StatusTone != pickOK {
		t.Errorf("tone = %v, want OK: a past failure is history, not the current state", row.StatusTone)
	}
	last := ""
	for _, line := range row.Info {
		if line.Label == "Last attempt" {
			last = line.Value
		}
	}
	for _, want := range []string{"2026-11-18 14:32", "call: boom"} {
		if !strings.Contains(last, want) {
			t.Errorf("last attempt = %q, want it to contain %q", last, want)
		}
	}
}

func TestExtensionHidesEmptyLastAttempt(t *testing.T) {
	s := extensions.Snapshot{Ref: extensions.SourceRef{Kind: "mcp", ID: "lark"}}
	for _, info := range [][]pickInfoLine{extensionSourceRow(s).Info, extensionFailureInfo(nil, s)} {
		for _, line := range info {
			if line.Label == "Last attempt" {
				t.Fatal("empty failure history must not be displayed")
			}
		}
	}
}

func TestExtensionSetupLabels(t *testing.T) {
	for _, tc := range []struct {
		ref  extensions.SourceRef
		want string
	}{
		{extensions.SourceRef{Kind: "mcp", ID: "lark"}, "Authorize: lark…"},
		{extensions.SourceRef{Kind: "mcp", ID: "other"}, "Configure / authorize…"},
		{extensions.SourceRef{Kind: "plugin", ID: "lark"}, "Configure / authorize…"},
	} {
		if got := extensionSetupLabel(tc.ref); got != tc.want {
			t.Errorf("extensionSetupLabel(%v) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}

func TestLarkProbeLabelKeepsUnknownDistinct(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  string
	}{
		{"authorized", "authorized"},
		{"required", "authorization required"},
		{"unavailable", "CLI unavailable"},
		{"unknown", "unknown"},
	} {
		got := larkProbeLabel(extensions.LarkAuthorizationStatus{
			Lark: extensions.LarkServiceAuthorization{State: tc.state},
		}, nil)
		if got != tc.want {
			t.Errorf("larkProbeLabel(%q) = %q, want %q", tc.state, got, tc.want)
		}
	}
	if got := larkProbeLabel(extensions.LarkAuthorizationStatus{}, errors.New("timeout")); got != "unknown (check failed)" {
		t.Errorf("failed probe label = %q, want it to stay unknown", got)
	}
}

func TestFinishLarkProbeUpdatesOnlyTheMatchingEpoch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := extensions.WriteSource(path, extensions.SourceRef{Kind: "mcp", ID: "lark"}, &extensions.SourceConfig{
		Enabled: true, Transport: "stdio", Command: "golder-lark",
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := extensions.New(extensions.Options{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	model := NewModel(Options{Extensions: manager, NoAnimations: true})
	model.larkProbeEpoch = 3
	model.menu.pickKind = "extensions"
	model.menu.pick = []pickItem{{
		Title: "lark [MCP · stdio]",
		Value: extensionValue("detail", "mcp", "lark"),
		Info:  []pickInfoLine{{Label: "Last attempt", Value: "old"}},
	}}

	done, _ := model.finishLarkProbe(larkProbeDoneMsg{epoch: 3, status: extensions.LarkAuthorizationStatus{
		Lark: extensions.LarkServiceAuthorization{State: "authorized"},
	}})
	got := done.(Model)
	if got.larkProbeStatus != "authorized" {
		t.Fatalf("probe status = %q, want authorized", got.larkProbeStatus)
	}
	if len(got.menu.pick) != 1 || !infoHas(got.menu.pick[0].Info, "Lark check", "authorized") {
		t.Fatalf("lark row was not updated: %+v", got.menu.pick[0].Info)
	}

	stale, _ := got.finishLarkProbe(larkProbeDoneMsg{epoch: 2, status: extensions.LarkAuthorizationStatus{}, err: errors.New("late")})
	if stale.(Model).larkProbeStatus != "authorized" {
		t.Fatal("a stale probe result replaced the current one")
	}
}

func infoHas(info []pickInfoLine, label, want string) bool {
	for _, line := range info {
		if line.Label == label && strings.Contains(line.Value, want) {
			return true
		}
	}
	return false
}

func pickTitles(rows []pickItem) string {
	var b strings.Builder
	for _, row := range rows {
		b.WriteString(row.Title + "\n")
	}
	return b.String()
}

// Esc walks back one picker level and only closes the menu at the top.
func TestExtensionDetailEscReturnsOneLevel(t *testing.T) {
	m := extensionModel(t)
	res, _ := m.runSlash("/extensions")
	m = res.(Model)
	res, _ = m.chooseExtension(extensionValue("detail", "mcp", "remote"))
	m = res.(Model)
	if !m.menu.picking() {
		t.Fatal("detail page did not open")
	}
	if hint := m.menu.hintText(); !strings.Contains(hint, "Esc back") {
		t.Fatalf("hint = %q, want it to say Esc goes back", hint)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.menu.picking() || !strings.Contains(pickTitles(m.menu.pick), "MCP servers") {
		t.Fatalf("Esc did not return to the list: %q", pickTitles(m.menu.pick))
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.menu.picking() {
		t.Fatal("Esc at the top should close the menu")
	}
}

// The primary page keeps the common actions; the noisy admin rows live in
// subpages, and both the Back row and Esc return without reopening the list.
func TestExtensionDetailKeepsAdvancedActionsOneLevelDown(t *testing.T) {
	m := extensionModel(t)
	res, _ := m.runSlash("/extensions")
	m = res.(Model)
	res, _ = m.chooseExtension(extensionValue("detail", "mcp", "remote"))
	m = res.(Model)

	primary := pickTitles(m.menu.pick)
	for _, hidden := range []string{"View logs", "Remove source…", "Disconnect", "Reconnect / restart", "Refresh catalog"} {
		if strings.Contains(primary, hidden) {
			t.Errorf("primary menu still shows %q: %s", hidden, primary)
		}
	}
	for _, shown := range []string{"Browse tools", "Connection…", "More…", "Back"} {
		if !strings.Contains(primary, shown) {
			t.Errorf("primary menu missing %q: %s", shown, primary)
		}
	}

	res, _ = m.chooseExtension(extensionValue("detail-page", "mcp", "remote", "more"))
	m = res.(Model)
	if !strings.Contains(pickTitles(m.menu.pick), "View logs") {
		t.Fatalf("More page lost the advanced actions: %q", pickTitles(m.menu.pick))
	}
	res, _ = m.chooseExtension(extensionValue("back"))
	m = res.(Model)
	if !strings.Contains(pickTitles(m.menu.pick), "Browse tools") {
		t.Fatalf("Back did not return to the detail page: %q", pickTitles(m.menu.pick))
	}
}
