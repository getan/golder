package agenttool

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/browserstartup"
)

func TestBrowserStartupExitsWithoutWaitingForReadinessTimeout(t *testing.T) {
	exit := make(chan browserPaneExit, 1)
	code := 139
	exit <- browserPaneExit{code: &code}
	start := time.Now()
	err := waitForBrowserStartup(context.Background(), paneSpec{Socket: filepath.Join(t.TempDir(), "missing.sock")}, 25*time.Second, exit)
	if err == nil || !strings.Contains(err.Error(), "139") {
		t.Fatalf("early exit=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("early exit waited for the readiness deadline")
	}
}

func TestBrowserStartupUsesFailureReportAndTerminalPhase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.json")
	r, err := browserstartup.New(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase("browser_connected")
	r.Phase("initializing_renderer")
	r.Fail(errors.New("no graphics support"), 1)
	err = waitForBrowserStartup(context.Background(), paneSpec{StartupReport: path, Socket: filepath.Join(t.TempDir(), "none.sock")}, 25*time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "terminal_initialization") || !strings.Contains(err.Error(), "no graphics support") {
		t.Fatalf("startup=%v", err)
	}
}

func TestBrowserStartupTimeoutDoesNotClaimBrowserIsRunningOrMissing(t *testing.T) {
	err := waitForBrowserStartup(context.Background(), paneSpec{Socket: filepath.Join(t.TempDir(), "none.sock")}, time.Millisecond, nil)
	if err == nil || strings.Contains(err.Error(), "is running") || strings.Contains(err.Error(), "check that Chromium is installed") {
		t.Fatalf("misleading startup error: %v", err)
	}
}
