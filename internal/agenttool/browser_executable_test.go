package agenttool

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestBrowserExecutableSettingSurvivesCleanEnvironment(t *testing.T) {
	exe := selfExecutable(t)
	withEmbeddedBrowser(t, exe)
	chrome := filepath.Join(t.TempDir(), "chrome with spaces")
	if err := os.WriteFile(chrome, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KITTY_BROWSE_CHROME", chrome)
	t.Setenv("GOLDER_SANDBOX", "auto")
	t.Setenv("OPENAI_API_KEY", "synthetic-not-a-real-key")
	argv, err := browserPaneArgv("control.sock", "private.profile", "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{exe, "browse", "-chrome", chrome, "-profile-dir", "private.profile", "-control-socket", "control.sock", "about:blank"}
	if !slices.Equal(argv, want) {
		t.Fatalf("browser argv=%q, want %q", argv, want)
	}
	g := NewControlGuard(t.TempDir())
	g.Runner = &controlRunner{}
	ctx := agentcore.WithBrowserRuntimeGrant(agentcore.WithNetworkGrant(context.Background()))
	clean, cleanup, _, err := g.launchArgvFor(ctx, argv, g.Root, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !slices.Contains(clean, chrome) {
		t.Fatal("explicit engine was lost while cleaning environment")
	}
	for _, arg := range clean {
		if strings.HasPrefix(arg, "OPENAI_API_KEY=") || strings.HasPrefix(arg, "KITTY_BROWSE_CHROME=") {
			t.Fatal("launcher forwarded ambient environment instead of a literal engine flag")
		}
	}
}

func TestBrowserExecutableSettingNeverFallsBackWhenInvalid(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	t.Setenv("KITTY_BROWSE_CHROME", filepath.Join(t.TempDir(), "missing-chromium"))
	if _, err := browserPaneArgv("control.sock", "private.profile", "about:blank"); err == nil || !strings.Contains(err.Error(), "KITTY_BROWSE_CHROME") {
		t.Fatalf("invalid explicit browser setting=%v", err)
	}
}

func TestBrowserExecutableSettingForExternalPane(t *testing.T) {
	withEmbeddedBrowser(t, "")
	dir := t.TempDir()
	bin := filepath.Join(dir, "kitty-browse")
	chrome := filepath.Join(dir, "headless-shell")
	for _, path := range []string{bin, chrome} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("KITTY_BROWSE_CHROME", chrome)
	argv, err := browserPaneArgv("control.sock", "private.profile", "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(argv, []string{bin, "-chrome", chrome, "-profile-dir", "private.profile", "-control-socket", "control.sock", "about:blank"}) {
		t.Fatalf("external argv=%q", argv)
	}
}
