package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func updateGitTestModel(t *testing.T, m *Model, msg tea.Msg) tea.Cmd {
	t.Helper()
	next, cmd := m.Update(msg)
	*m = next.(Model)
	return cmd
}

// probeFromBatch executes only the immediate Git probe, not the timer beside
// it. Bubble Tea executes the children independently in the running program.
func probeFromBatch(t *testing.T, cmd tea.Cmd) gitInfoMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing Git command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("expected probe and timer, got %#v", batch)
	}
	msg, ok := batch[0]().(gitInfoMsg)
	if !ok {
		t.Fatal("first batch command is not the Git probe")
	}
	return msg
}

func gitTestEnv(t *testing.T, workspace bool) {
	t.Helper()
	t.Setenv("GOLDER_WORKSPACE", "")
	t.Setenv("TUIOS_PANE_ID", "")
	if workspace {
		t.Setenv("GOLDER_WORKSPACE", "1")
		t.Setenv("TUIOS_PANE_ID", "pane")
	}
}

func TestGitRefreshStandaloneFocusAndFallback(t *testing.T) {
	gitTestEnv(t, false)
	m := NewModel(Options{})
	if !m.View().ReportFocus || !m.gitRefresh.active {
		t.Fatal("standalone TUI should request focus events and poll until told otherwise")
	}
	m.extensionSetupText = "authorization"
	if !m.View().ReportFocus {
		t.Fatal("authorization view dropped focus reporting")
	}
	m.extensionSetupText = ""
	cmd := updateGitTestModel(t, &m, gitRefreshStartMsg{})
	if cmd == nil || !m.gitRefresh.initialized || !m.gitRefresh.inFlight || m.gitRefresh.tickGen != 1 {
		t.Fatalf("standalone startup did not start its probe and timer: %+v", m.gitRefresh)
	}
	updateGitTestModel(t, &m, gitInfoMsg{request: m.gitRefresh.request, dir: m.cwd, ok: true, branch: "main"})
	if !m.statusBar.git.ok || m.statusBar.git.branch != "main" {
		t.Fatal("initial Git reading not applied")
	}
	oldGen := m.gitRefresh.tickGen
	updateGitTestModel(t, &m, tea.BlurMsg{})
	if m.gitRefresh.active || m.gitRefresh.tickGen == oldGen {
		t.Fatalf("blur did not invalidate timer: %+v", m.gitRefresh)
	}
	if cmd := updateGitTestModel(t, &m, gitRefreshTickMsg{gen: oldGen}); cmd != nil || m.gitRefresh.inFlight {
		t.Fatal("stale timer refreshed a blurred terminal")
	}
	if cmd := updateGitTestModel(t, &m, tea.FocusMsg{}); cmd == nil || !m.gitRefresh.active || !m.gitRefresh.inFlight {
		t.Fatalf("focus return did not immediately refresh: %+v", m.gitRefresh)
	}
	request, gen := m.gitRefresh.request, m.gitRefresh.tickGen
	if cmd := updateGitTestModel(t, &m, tea.FocusMsg{}); cmd != nil || m.gitRefresh.request != request || m.gitRefresh.tickGen != gen {
		t.Fatal("duplicate focus started another probe or timer")
	}
}

func TestGitRefreshWorkspaceOnlyFocusedPanePolls(t *testing.T) {
	gitTestEnv(t, true)
	left, right := NewModel(Options{}), NewModel(Options{})
	for _, m := range []*Model{&left, &right} {
		if m.gitRefresh.active || !m.View().ReportFocus {
			t.Fatalf("workspace pane must start unfocused and ask for reports: %+v", m.gitRefresh)
		}
		cmd := updateGitTestModel(t, m, gitRefreshStartMsg{})
		if cmd == nil || m.gitRefresh.tickGen != 0 || !m.gitRefresh.inFlight {
			t.Fatalf("hidden pane must get only its initial probe: %+v", m.gitRefresh)
		}
		updateGitTestModel(t, m, gitInfoMsg{request: m.gitRefresh.request, dir: m.cwd, ok: true})
	}
	if cmd := updateGitTestModel(t, &left, tea.FocusMsg{}); cmd == nil || !left.gitRefresh.inFlight || right.gitRefresh.active {
		t.Fatal("only the focused pane should refresh")
	}
	updateGitTestModel(t, &left, gitInfoMsg{request: left.gitRefresh.request, dir: left.cwd, ok: true})
	leftGen := left.gitRefresh.tickGen
	updateGitTestModel(t, &left, tea.BlurMsg{})
	updateGitTestModel(t, &right, tea.FocusMsg{})
	if left.gitRefresh.active || !right.gitRefresh.active || !right.gitRefresh.inFlight {
		t.Fatalf("pane focus did not transfer: left=%+v right=%+v", left.gitRefresh, right.gitRefresh)
	}
	if cmd := updateGitTestModel(t, &left, gitRefreshTickMsg{gen: leftGen}); cmd != nil || left.gitRefresh.inFlight {
		t.Fatal("background pane retained its periodic refresh")
	}
}

func TestGitRefreshFocusBeforeStart(t *testing.T) {
	gitTestEnv(t, true)
	m := NewModel(Options{})
	if cmd := updateGitTestModel(t, &m, tea.FocusMsg{}); cmd != nil || !m.gitRefresh.active {
		t.Fatal("early focus should be remembered until startup")
	}
	if cmd := updateGitTestModel(t, &m, gitRefreshStartMsg{}); cmd == nil || !m.gitRefresh.inFlight || m.gitRefresh.tickGen == 0 {
		t.Fatal("startup lost the earlier focus event")
	}
}

func TestGitRefreshRunsDuringTurnAndOnceAfterHiddenTurn(t *testing.T) {
	gitTestEnv(t, true)
	m := NewModel(Options{})
	updateGitTestModel(t, &m, tea.FocusMsg{})
	updateGitTestModel(t, &m, gitRefreshStartMsg{})
	updateGitTestModel(t, &m, gitInfoMsg{request: m.gitRefresh.request, dir: m.cwd, ok: true})
	m.running = true
	if cmd := updateGitTestModel(t, &m, gitRefreshTickMsg{gen: m.gitRefresh.tickGen}); cmd == nil || !m.gitRefresh.inFlight {
		t.Fatal("an active turn must not suspend the focused pane's refresh")
	}
	old := m.gitRefresh.request
	updateGitTestModel(t, &m, tea.BlurMsg{})
	updateGitTestModel(t, &m, runEndMsg{})
	if m.gitRefresh.active || !m.gitRefresh.pending || !m.gitRefresh.inFlight {
		t.Fatalf("hidden turn end did not queue a final reading: %+v", m.gitRefresh)
	}
	if cmd := updateGitTestModel(t, &m, gitInfoMsg{request: old, dir: m.cwd, ok: true}); cmd == nil || !m.gitRefresh.inFlight || m.gitRefresh.request == old {
		t.Fatal("hidden turn end did not receive its one trailing reading")
	}
	updateGitTestModel(t, &m, gitInfoMsg{request: m.gitRefresh.request, dir: m.cwd, ok: true})
	if m.gitRefresh.active || m.gitRefresh.inFlight || m.gitRefresh.pending {
		t.Fatal("hidden pane did not settle after the turn-end reading")
	}
}

func TestGitRefreshCoalescesAndRejectsStaleReadings(t *testing.T) {
	gitTestEnv(t, false)
	m := NewModel(Options{})
	if m.requestGitRefresh(false) == nil {
		t.Fatal("initial request did not start")
	}
	first := m.gitRefresh.request
	if m.requestGitRefresh(false) != nil || m.gitRefresh.pending {
		t.Fatal("periodic tick should not queue duplicate work")
	}
	if m.requestGitRefresh(true) != nil || !m.gitRefresh.pending {
		t.Fatal("turn end or focus return must request one trailing read")
	}
	if cmd := m.finishGitRefresh(gitInfoMsg{request: first + 1, dir: m.cwd, ok: true, branch: "stale"}); cmd != nil || !m.gitRefresh.inFlight {
		t.Fatal("unrelated response changed the active request")
	}
	if cmd := m.finishGitRefresh(gitInfoMsg{request: first, dir: m.cwd, ok: true, branch: "first"}); cmd == nil || m.gitRefresh.request != first+1 || !m.gitRefresh.inFlight || m.gitRefresh.pending {
		t.Fatalf("one trailing read was not started: %+v", m.gitRefresh)
	}
	if cmd := m.finishGitRefresh(gitInfoMsg{request: first, dir: m.cwd, ok: true, branch: "old"}); cmd != nil || m.statusBar.git.branch != "first" {
		t.Fatal("an old result replaced the newer request's state")
	}
	m.finishGitRefresh(gitInfoMsg{request: first + 1, dir: m.cwd, ok: true, branch: "new"})
	if m.statusBar.git.branch != "new" || m.gitRefresh.inFlight {
		t.Fatal("the trailing reading did not settle")
	}
	if m.requestGitRefresh(false) == nil {
		t.Fatal("next read did not start")
	}
	oldDir, id := m.cwd, m.gitRefresh.request
	m.cwd = filepath.Join(oldDir, "other")
	if cmd := m.finishGitRefresh(gitInfoMsg{request: id, dir: oldDir, ok: true, branch: "wrong-dir"}); cmd == nil || m.statusBar.git.branch != "new" || !m.gitRefresh.inFlight {
		t.Fatal("old directory response was applied instead of starting a fresh read")
	}
}

func TestGitRefreshBacksOffFailures(t *testing.T) {
	for i, want := range []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute} {
		if got := gitRefreshDelay(i); got != want {
			t.Errorf("failure count %d: delay=%v, want %v", i, got, want)
		}
	}
	gitTestEnv(t, false)
	m := NewModel(Options{})
	for i := 1; i <= 4; i++ {
		m.requestGitRefresh(false)
		m.finishGitRefresh(gitInfoMsg{request: m.gitRefresh.request, dir: m.cwd})
		if want := min(i, 3); m.gitRefresh.failures != want {
			t.Fatalf("failure count=%d, want %d", m.gitRefresh.failures, want)
		}
	}
	m.requestGitRefresh(false)
	m.finishGitRefresh(gitInfoMsg{request: m.gitRefresh.request, dir: m.cwd, ok: true})
	if m.gitRefresh.failures != 0 {
		t.Fatal("successful reading did not reset backoff")
	}
}

func TestGitProbeBufferIsBounded(t *testing.T) {
	buffer := gitProbeBuffer{limit: 4}
	for _, text := range []string{"abc", "def", "g"} {
		if n, err := buffer.Write([]byte(text)); err != nil || n != len(text) {
			t.Fatalf("write %q = %d, %v", text, n, err)
		}
	}
	if got := buffer.text.String(); got != "abcd" || !buffer.truncated {
		t.Fatalf("bounded output = %q, truncated=%t", got, buffer.truncated)
	}
}

func TestGitProbeTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Git executable uses a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nexec /bin/sleep 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	start := time.Now()
	_, err := runGit(dir, "status")
	if err == nil {
		t.Fatal("a stuck Git command must time out")
	}
	if errors.Is(err, os.ErrPermission) {
		t.Skipf("sandbox refused to terminate the test subprocess: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Git probe was not bounded: %v, error: %v", elapsed, err)
	}
}

func TestGitRefreshReschedulesWhenBackoffChanges(t *testing.T) {
	gitTestEnv(t, false)
	m := NewModel(Options{})
	updateGitTestModel(t, &m, gitRefreshStartMsg{})
	oldGen := m.gitRefresh.tickGen
	if cmd := updateGitTestModel(t, &m, gitInfoMsg{request: m.gitRefresh.request, dir: m.cwd}); cmd == nil || m.gitRefresh.tickDelay != 30*time.Second || m.gitRefresh.tickGen == oldGen {
		t.Fatalf("failure did not replace the timer with backoff: %+v", m.gitRefresh)
	}
	if cmd := updateGitTestModel(t, &m, gitRefreshTickMsg{gen: oldGen}); cmd != nil || m.gitRefresh.inFlight {
		t.Fatal("old fast timer remained active after failure")
	}
	if cmd := updateGitTestModel(t, &m, gitRefreshTickMsg{gen: m.gitRefresh.tickGen}); cmd == nil || !m.gitRefresh.inFlight {
		t.Fatal("backoff timer did not start the retry")
	}
	oldGen = m.gitRefresh.tickGen
	if cmd := updateGitTestModel(t, &m, gitInfoMsg{request: m.gitRefresh.request, dir: m.cwd, ok: true}); cmd == nil || m.gitRefresh.tickDelay != gitRefreshInterval || m.gitRefresh.tickGen == oldGen {
		t.Fatalf("success did not restore the normal timer: %+v", m.gitRefresh)
	}
}

func TestGitRefreshFindsExternalCommitWhileIdle(t *testing.T) {
	gitTestEnv(t, false)
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		if value, ok := os.LookupEnv(name); ok {
			t.Setenv(name, value)
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.name", "test")
	git("config", "user.email", "test@example.invalid")
	file := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "file.txt")
	git("commit", "-qm", "initial")
	if err := os.WriteFile(file, []byte("modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewModel(Options{})
	m.cwd = repo
	first := probeFromBatch(t, updateGitTestModel(t, &m, gitRefreshStartMsg{}))
	updateGitTestModel(t, &m, first)
	if !m.statusBar.git.ok || m.statusBar.git.dirty != 1 {
		t.Fatalf("expected modified file, got %+v", m.statusBar.git)
	}
	// A second process commits without a TUI turn or focus change.
	git("add", "file.txt")
	git("commit", "-qm", "external")
	second := probeFromBatch(t, updateGitTestModel(t, &m, gitRefreshTickMsg{gen: m.gitRefresh.tickGen}))
	updateGitTestModel(t, &m, second)
	if !m.statusBar.git.ok || m.statusBar.git.dirty != 0 {
		t.Fatalf("idle timer kept stale dirty status after external commit: %+v", m.statusBar.git)
	}
}
