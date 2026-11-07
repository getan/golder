package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/selfupdate"
)

func TestUpdatePromptKeys(t *testing.T) {
	options := []string{"Update now", "Skip this time", "Skip this version"}
	for _, tc := range []struct {
		name string
		keys []tea.KeyPressMsg
		want updateChoice
	}{
		{"enter updates", []tea.KeyPressMsg{{Code: tea.KeyEnter}}, choiceUpdate},
		{"down skips once", []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: tea.KeyEnter}}, choiceSkipOnce},
		{"number skips version", []tea.KeyPressMsg{{Code: '3', Text: "3"}, {Code: tea.KeyEnter}}, choiceSkipVersion},
		{"escape skips once", []tea.KeyPressMsg{{Code: tea.KeyEsc}}, choiceSkipOnce},
		{"interrupt", []tea.KeyPressMsg{{Code: 'c', Mod: tea.ModCtrl}}, choiceInterrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := updatePrompt{heading: "update", options: options, cancel: choiceSkipOnce}
			for _, key := range tc.keys {
				next, _ := m.Update(key)
				m = next.(updatePrompt)
			}
			if !m.done || m.choice != tc.want {
				t.Fatalf("got choice %v done=%t, want %v", m.choice, m.done, tc.want)
			}
		})
	}
	view := (updatePrompt{heading: "update", options: options}).View().Content
	for _, option := range options {
		if !strings.Contains(view, option) {
			t.Errorf("missing %q in prompt", option)
		}
	}
}

func TestStartupUpdateDoesNotPromptAutomation(t *testing.T) {
	for _, opts := range []cliOptions{
		{prompt: "hello"}, {printProvided: true}, {outputFmt: "stream-json"},
		{subagentRPC: true}, {dream: true}, {githubReview: true}, {listSessions: true}, {noUpdateCheck: true},
	} {
		if startupUpdateMode(opts, "", "") {
			t.Errorf("allowed automation mode %+v", opts)
		}
	}
	if !startupUpdateMode(cliOptions{}, "", "") {
		t.Fatal("ordinary interactive mode should be eligible")
	}
	if startupUpdateMode(cliOptions{}, "true", "") || startupUpdateMode(cliOptions{}, "", "1") {
		t.Fatal("CI and disabled checks must not prompt")
	}
	if startupUpdateAllowed(cliOptions{}, io.Discard) {
		t.Fatal("non-terminal output must not prompt")
	}
	t.Setenv("CI", "true")
	if startupUpdateAllowed(cliOptions{}, os.Stdout) {
		t.Fatal("CI must not prompt")
	}
}

func TestStartupUpdateConfigPrecedence(t *testing.T) {
	disabled := false
	var opts cliOptions
	applyFileConfig(&opts, config.FileConfig{UpdateCheck: &disabled}, func(string) bool { return false })
	if !opts.noUpdateCheck {
		t.Fatal("configuration should disable update checks")
	}
	opts.noUpdateCheck = false
	applyFileConfig(&opts, config.FileConfig{UpdateCheck: &disabled}, func(s string) bool { return s == "no-update-check" })
	if opts.noUpdateCheck {
		t.Fatal("explicit CLI flag must override configuration")
	}
}

func TestClassifyInstall(t *testing.T) {
	t.Setenv("GOLDER_INSTALL_DIR", "")
	if got := classifyInstall("/opt/homebrew/Cellar/golder/1.0.0/bin/golder"); got != installHomebrew {
		t.Fatalf("Homebrew = %v", got)
	}
	if got := classifyInstall("/usr/bin/golder"); got != installManaged {
		t.Fatalf("system package = %v", got)
	}
	if got := classifyInstall(filepath.Join(t.TempDir(), "golder")); got != installManaged {
		t.Fatalf("unknown install = %v", got)
	}
	if runtime.GOOS != "windows" {
		dir := t.TempDir()
		t.Setenv("GOLDER_INSTALL_DIR", dir)
		if got := classifyInstall(filepath.Join(dir, "golder")); got != installStandalone {
			t.Fatalf("explicit standalone = %v", got)
		}
	}
}

func TestHomebrewTargetMustMatchRunningExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based Homebrew fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	prefix := filepath.Join(dir, "prefix")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	brew := filepath.Join(bin, "brew")
	if err := os.WriteFile(brew, []byte("#!/bin/sh\nprintf '%s\\n' \"$TEST_BREW_PREFIX\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TEST_BREW_PREFIX", prefix)
	actual := filepath.Join(dir, "actual-golder")
	if err := os.WriteFile(actual, []byte("#!/bin/sh\nprintf 'golder v1.2.3\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, filepath.Join(prefix, "bin", "golder")); err != nil {
		t.Fatal(err)
	}
	actual, err := filepath.EvalSymlinks(actual)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyHomebrewTarget(context.Background(), actual); err != nil {
		t.Fatalf("matching installation refused: %v", err)
	}
	if err := verifyHomebrewTarget(context.Background(), filepath.Join(dir, "other-golder")); err == nil {
		t.Fatal("different Homebrew installation must be refused")
	}
	if err := verifyUpdatedExecutable(context.Background(), actual, "v1.2.3", installStandalone); err != nil {
		t.Fatalf("matching version rejected: %v", err)
	}
	if err := verifyUpdatedExecutable(context.Background(), actual, "v1.2.4", installStandalone); err == nil {
		t.Fatal("old executable incorrectly verified as updated")
	}
}

func TestStartupUpdateChoices(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows installations use manual update")
	}
	for _, tc := range []struct {
		name          string
		choices       []updateChoice
		installErr    error
		skipErr       error
		wantDone      bool
		wantCode      int
		wantInstalled bool
		wantSkipped   bool
		wantError     string
	}{
		{"skip once", []updateChoice{choiceSkipOnce}, nil, nil, false, 0, false, false, ""},
		{"skip version", []updateChoice{choiceSkipVersion}, nil, nil, false, 0, false, true, ""},
		{"skip write error", []updateChoice{choiceSkipVersion}, nil, errors.New("disk"), false, 0, false, true, "could not remember"},
		{"interrupt", []updateChoice{choiceInterrupt}, nil, nil, true, 130, false, false, ""},
		{"update", []updateChoice{choiceUpdate}, nil, nil, true, 0, true, false, ""},
		{"failure continue", []updateChoice{choiceUpdate, choiceUpdate}, errors.New("network"), nil, false, 0, true, false, "network"},
		{"failure exit", []updateChoice{choiceUpdate, choiceSkipOnce}, errors.New("network"), nil, true, 1, true, false, "network"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			installed, skipped, n := false, false, 0
			services := startupUpdateServices{
				latest: func(string, time.Duration) string { return "v1.0.2" },
				offer:  func(string, string) bool { return true },
				updater: func() (*selfupdate.Updater, error) {
					return &selfupdate.Updater{ExecPath: "/usr/local/bin/golder"}, nil
				},
				choose: func(_ io.Reader, _ io.Writer, _ string, _ []string, _ updateChoice) (updateChoice, error) {
					choice := tc.choices[n]
					n++
					return choice, nil
				},
				skip: func(tag string) error {
					skipped = tag == "v1.0.2"
					return tc.skipErr
				},
				install: func(_ context.Context, _ *selfupdate.Updater, _ installKind, _ string, _ io.Reader, _, _ io.Writer) error {
					installed = true
					return tc.installErr
				},
			}
			done, code := runStartupUpdate(context.Background(), "v1.0.0", strings.NewReader(""), &out, &errOut, services)
			if done != tc.wantDone || code != tc.wantCode || installed != tc.wantInstalled || skipped != tc.wantSkipped {
				t.Fatalf("done=%t code=%d installed=%t skipped=%t", done, code, installed, skipped)
			}
			if !strings.Contains(errOut.String(), tc.wantError) {
				t.Fatalf("error output %q lacks %q", errOut.String(), tc.wantError)
			}
			if installed && tc.installErr == nil && !strings.Contains(out.String(), "Run your command again") {
				t.Fatalf("success must ask the user to restart: %q", out.String())
			}
		})
	}
}
