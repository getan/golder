package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/session"
)

// TestResolveResumeID pins the unfiltered numbering (an empty projectDir
// degrades to --all) and the literal-id passthrough.
func TestResolveResumeID(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	base := time.Now().UTC()
	mk := func(model string, at time.Time) string {
		h := session.SessionHeader{ID: session.NewID(at), CreatedAt: at, UpdatedAt: at, Model: model}
		if err := store.Save(h, nil); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return h.ID
	}
	idOld := mk("old", base)
	idNew := mk("new", base.Add(time.Second))

	if got, err := ResolveResumeID(store, "1", ""); err != nil || got != idNew {
		t.Errorf("1 = (%q, %v), want (%q, nil)", got, err, idNew)
	}
	if got, err := ResolveResumeID(store, "2", ""); err != nil || got != idOld {
		t.Errorf("2 = (%q, %v), want (%q, nil)", got, err, idOld)
	}
	if _, err := ResolveResumeID(store, "3", ""); err == nil {
		t.Error("3 should be out of range")
	}
	if _, err := ResolveResumeID(store, "0", ""); err == nil {
		t.Error("0 should be out of range")
	}
	if got, err := ResolveResumeID(store, idOld, ""); err != nil || got != idOld {
		t.Errorf("literal id = (%q, %v), want passthrough", got, err)
	}
	if _, err := ResolveResumeID(store, "", ""); err == nil {
		t.Error("empty arg should be a usage error")
	}
	if _, err := ResolveResumeID(store, "--all", ""); err == nil {
		t.Error("a bare --all carries no selector and should be a usage error")
	}
}

// TestParseResumeArg: the --all/-a flag lifts the project filter wherever it
// appears, and the selector is whatever is left over.
func TestParseResumeArg(t *testing.T) {
	cases := []struct {
		arg     string
		wantAll bool
		wantSel string
	}{
		{"", false, ""},
		{"2", false, "2"},
		{"--all", true, ""},
		{"-a", true, ""},
		{"--all 3", true, "3"},
		{"3 --all", true, "3"},
		{"-a sess-1", true, "sess-1"},
		{"sess-1", false, "sess-1"},
	}
	for _, tc := range cases {
		scope, sel := ParseResumeArg(tc.arg, "/proj")
		if scope.All != tc.wantAll || sel != tc.wantSel || scope.Dir != "/proj" {
			t.Errorf("ParseResumeArg(%q) = (%+v, %q), want (All=%v, Dir=/proj, sel=%q)",
				tc.arg, scope, sel, tc.wantAll, tc.wantSel)
		}
	}
}

// TestResumeScopeMatch: a scope anchored in a repository matches sessions from
// that repository's other directories, rejects unrelated projects and
// unattributed (pre-Cwd) sessions, and degrades to --all without an anchor.
func TestResumeScopeMatch(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "cmd")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	h := func(cwd string) session.SessionHeader { return session.SessionHeader{Cwd: cwd} }

	scope := ProjectScope(sub)
	if !scope.Match(h(repo)) || !scope.Match(h(sub)) {
		t.Error("sessions from the same repository must match, root or subdir")
	}
	if scope.Match(h(other)) {
		t.Error("a session from another project must not match")
	}
	if scope.Match(h("")) {
		t.Error("an unattributed session must not match a project scope")
	}
	if !AllSessionsScope().Match(h("")) {
		t.Error("--all must list unattributed sessions")
	}
	if !ProjectScope("").Match(h(other)) {
		t.Error("a scope without an anchor must degrade to --all")
	}
}

// TestResolveResumeIDScoped: numbering follows the scoped list, an out-of-range
// number explains the filter, and a literal id still resolves globally.
func TestResolveResumeIDScoped(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	proj := t.TempDir()
	other := t.TempDir()
	base := time.Now().UTC()
	save := func(cwd string, at time.Time) string {
		h := session.SessionHeader{ID: session.NewID(at), CreatedAt: at, UpdatedAt: at, Cwd: cwd}
		if err := store.Save(h, nil); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return h.ID
	}
	older := save(proj, base.Add(-time.Hour))
	newer := save(proj, base.Add(-time.Minute))
	foreign := save(other, base) // globally newest, other project

	if got, err := ResolveResumeID(store, "1", proj); err != nil || got != newer {
		t.Errorf("scoped 1 = (%q, %v), want (%q, nil)", got, err, newer)
	}
	if got, err := ResolveResumeID(store, "2", proj); err != nil || got != older {
		t.Errorf("scoped 2 = (%q, %v), want (%q, nil)", got, err, older)
	}
	_, err = ResolveResumeID(store, "3", proj)
	if err == nil || !strings.Contains(err.Error(), AllSessionsFlag) {
		t.Errorf("scoped 3 error = %v, want a hint naming %s", err, AllSessionsFlag)
	}
	if got, err := ResolveResumeID(store, "--all 1", proj); err != nil || got != foreign {
		t.Errorf("--all 1 = (%q, %v), want (%q, nil)", got, err, foreign)
	}
	// A literal id is unambiguous, so the project filter never hides it.
	if got, err := ResolveResumeID(store, foreign, proj); err != nil || got != foreign {
		t.Errorf("literal foreign id = (%q, %v), want passthrough", got, err)
	}
}

// TestResumeHints: an empty or shortened scoped list says where the rest are.
func TestResumeHints(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	proj := t.TempDir()
	other := t.TempDir()
	base := time.Now().UTC()
	save := func(cwd string, at time.Time, model string) {
		h := session.SessionHeader{ID: session.NewID(at), CreatedAt: at, UpdatedAt: at, Cwd: cwd, Model: model}
		if err := store.Save(h, nil); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	scope := ProjectScope(proj)

	// Empty project, other projects hold sessions.
	save(other, base, "m")
	if msg := ResumeEmptyMessage(store, scope); !strings.Contains(msg, AllSessionsFlag) {
		t.Errorf("empty-scope message = %q, want a hint naming %s", msg, AllSessionsFlag)
	}
	// One in project, one elsewhere: the hidden hint counts the outsider.
	save(proj, base.Add(time.Minute), "m")
	if hint := ResumeHiddenHint(store, scope, 1); !strings.Contains(hint, "1 session(s) in other projects") {
		t.Errorf("hidden hint = %q, want the other-project count", hint)
	}
	// Unfiltered views never hint.
	if hint := ResumeHiddenHint(store, AllSessionsScope(), 2); hint != "" {
		t.Errorf("--all hint = %q, want empty", hint)
	}
	if msg := ResumeEmptyMessage(store, AllSessionsScope()); msg != "No saved sessions yet." {
		t.Errorf("empty --all message = %q", msg)
	}
	// The store's rows show a home-relative directory (or "unknown").
	if got := SessionDirDisplay(session.SessionHeader{Cwd: proj}); got != DisplayHomePath(proj) {
		t.Errorf("SessionDirDisplay = %q, want %q", got, DisplayHomePath(proj))
	}
	if got := SessionDirDisplay(session.SessionHeader{}); got != "unknown" {
		t.Errorf("SessionDirDisplay(no cwd) = %q, want unknown", got)
	}
}

func TestSessionPreview(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	textMsg := func(role string, text string) agentcore.Message {
		c := agentcore.ContentList{agentcore.NewTextContent(text)}
		if role == agentcore.RoleUser {
			return agentcore.UserMessage{RoleField: role, Content: c}
		}
		return agentcore.AssistantMessage{RoleField: role, Content: c}
	}
	save := func(id string, msgs ...agentcore.Message) {
		now := time.Now().UTC()
		h := session.SessionHeader{ID: id, CreatedAt: now, UpdatedAt: now}
		var list agentcore.MessageList
		for _, m := range msgs {
			list = append(list, m)
		}
		if err := store.Save(h, list); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	save("s-user", textMsg(agentcore.RoleUser, "帮我看看这个bug\n第二行"))
	save("s-asst", textMsg(agentcore.RoleAssistant, "assistant only"))
	save("s-empty")
	save("s-long", textMsg(agentcore.RoleUser, "这是一个很长的中文问题描述，用来验证按rune截断不会把汉字切半，后面再补几个字凑够四十"))

	if got := SessionPreview(store, "s-user"); got != "帮我看看这个bug 第二行" {
		t.Errorf("user preview = %q", got)
	}
	if got := SessionPreview(store, "s-asst"); got != "assistant only" {
		t.Errorf("assistant fallback = %q", got)
	}
	if got := SessionPreview(store, "s-empty"); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
	got := SessionPreview(store, "s-long")
	if r := []rune(got); len(r) != previewRunes+1 || string(r[len(r)-1]) != "…" {
		t.Errorf("long preview should be %d runes + …, got %q", previewRunes, got)
	}
	if got := SessionPreview(store, "nope"); got != "" {
		t.Errorf("missing session = %q, want empty", got)
	}

	items, err := RecentSessionsWithPreview(store, AllSessionsScope(), 10)
	if err != nil || len(items) != 4 {
		t.Fatalf("RecentSessionsWithPreview = (%d, %v), want (4, nil)", len(items), err)
	}
}
