package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func clearMotionEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"GOLDER_NO_ANIMATIONS", "SSH_TTY", "SSH_CONNECTION"} {
		t.Setenv(key, "")
	}
}

func TestTUIMotionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		flag, disabled   bool
	}{
		{name: "local default"},
		{name: "flag", flag: true, disabled: true},
		{name: "env", key: "GOLDER_NO_ANIMATIONS", value: "1", disabled: true},
		{name: "env true", key: "GOLDER_NO_ANIMATIONS", value: "true", disabled: true},
		{name: "env false", key: "GOLDER_NO_ANIMATIONS", value: "0"},
		{name: "SSH tty", key: "SSH_TTY", value: "/dev/pts/0", disabled: true},
		{name: "SSH connection", key: "SSH_CONNECTION", value: "client 123 server 22", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearMotionEnv(t)
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			withEmptyTrustStore(t)
			opts := Options{Approve: true, NoAnimations: tc.flag}
			s, _, err := newRunSessionWithStore(newTestStore(t), opts)
			if err != nil {
				t.Fatal(err)
			}
			m := NewModel(opts).withSession(s, nil)
			if m.opts.NoAnimations != tc.disabled || m.logoRunning == tc.disabled {
				t.Fatalf("disabled=%v logoRunning=%v; want disabled=%v", m.opts.NoAnimations, m.logoRunning, tc.disabled)
			}
			if tc.disabled {
				want := renderBannerFrame(m.theme, m.bannerOptions(), m.cwd, logoFrames)
				assertSettledBanner(t, m, want)
				got, cmd := m.Update(logoFrameMsg{frame: 1})
				if cmd != nil || got.(Model).logoRunning {
					t.Fatal("disabled logo must not arm a tick")
				}
			}
		})
	}
}

func assertSettledBanner(t *testing.T, m Model, want string) {
	t.Helper()
	// The first transcript block is the startup banner.
	if len(m.transcript.blocks) == 0 || m.transcript.blocks[0].text != want {
		t.Fatal("startup banner must show the settled logo")
	}
}

func TestFirstInputSettlesLogoWithoutEatingInput(t *testing.T) {
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: 'x', Text: "x"}, tea.PasteMsg{Content: "x"}} {
		clearMotionEnv(t)
		m := NewModel(Options{})
		m.transcript.addBanner(renderBannerFrame(m.theme, m.bannerOptions(), m.cwd, 0))
		m.logoRunning = true
		got, _ := m.Update(msg)
		m = got.(Model)
		if m.logoRunning || m.input.Value() != "x" {
			t.Fatalf("first input %T: logoRunning=%v input=%q", msg, m.logoRunning, m.input.Value())
		}
		want := renderBannerFrame(m.theme, m.bannerOptions(), m.cwd, logoFrames)
		assertSettledBanner(t, m, want)
		got, cmd := m.Update(logoFrameMsg{frame: 2})
		if cmd != nil {
			t.Fatal("stale logo frame must not schedule another tick")
		}
		assertSettledBanner(t, got.(Model), want)
	}
}

func TestNoAnimationsKeepsWorkingIndicatorStatic(t *testing.T) {
	m := NewModel(Options{NoAnimations: true})
	m.running = true
	m.spinner.begin(time.Now(), "medium")
	before := m.spinner
	got, cmd := m.Update(spinnerTickMsg(time.Now()))
	m = got.(Model)
	if m.spinner.frame != before.frame || m.spinner.verb != before.verb {
		t.Fatal("reduced-motion spinner must not animate or reroll its label")
	}
	if cmd == nil {
		t.Fatal("elapsed-time updates must remain live")
	}
	m.width = 20
	m.statusBar.cwd = "~/a/very/long/path/to/the/project"
	if m.startStatusReveal() != nil {
		t.Fatal("reduced-motion status bar must not schedule scrolling")
	}
}
