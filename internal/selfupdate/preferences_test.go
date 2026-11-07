package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/getan/golder/internal/golderhome"
)

func TestStartupOfferAndSkippedVersion(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if !StartupOffer("1.0.0", "v1.0.2") {
		t.Fatal("newer version should be offered")
	}
	if err := SkipVersion("v1.0.2"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"v1.0.0", "v1.0.2", false},
		{"v1.0.0", "v1.0.1", false},
		{"v1.0.0", "v1.0.3", true},
		{"v1.0.3", "v1.0.3", false},
		{"dev", "v1.0.3", false},
		{"v1.0.0", "v1.0.3/../../bad", false},
		{"v1.0.0", "v1.0.3-beta", false},
	} {
		if got := StartupOffer(tc.current, tc.latest); got != tc.want {
			t.Errorf("StartupOffer(%q,%q) = %t, want %t", tc.current, tc.latest, got, tc.want)
		}
	}
	path := filepath.Join(os.Getenv("GOLDER_HOME"), golderhome.UpdatePrefsFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("preferences must be private: %v", info.Mode())
	}
	if err := SkipVersion("../invalid"); err == nil {
		t.Fatal("invalid version should not be saved")
	}
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !StartupOffer("v1.0.0", "v1.0.2") {
		t.Fatal("corrupt preferences should not prevent startup offers")
	}
}

func TestLatestForStartupUsesFreshCache(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	writeCache("v1.0.2")
	if got := LatestForStartup("v1.0.0", time.Second); got != "v1.0.2" {
		t.Fatalf("fresh cache = %q", got)
	}
	if got := LatestForStartup("dev", time.Second); got != "" {
		t.Fatalf("dev cache = %q", got)
	}
}

func TestLatestForStartupTimeoutStillCaches(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	release := make(chan struct{})
	saved := make(chan string, 1)
	got := latestForStartup("v1.0.0", 0, func(ctx context.Context, _ string) (string, error) {
		select {
		case <-release:
			return "v1.0.2", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}, func(tag string) { saved <- tag })
	if got != "" {
		t.Fatalf("slow check blocked or returned a stale tag: %q", got)
	}
	close(release)
	select {
	case tag := <-saved:
		if tag != "v1.0.2" {
			t.Fatalf("saved = %q", tag)
		}
	case <-time.After(time.Second):
		t.Fatal("background result was not saved")
	}
}

func TestSkipVersionFailsWithoutWritableState(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOLDER_HOME", file)
	if err := SkipVersion("v1.0.2"); err == nil {
		t.Fatal("expected an error when state cannot be saved")
	}
}
