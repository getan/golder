package browserstartup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportBoundedRedactedAndStartupOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.json")
	r, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase("starting_chromium")
	secret := "sk-proj-" + strings.Repeat("aB2cD3eF4", 6)
	_, _ = r.Write([]byte("FATAL: key=" + secret + "\n" + strings.Repeat("diagnostic\n", 5000)))
	r.Fail(errors.New("signal: trace/BPT trap at https://example.com/?private-query=yes"), 1)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "private-query") {
		t.Fatal("startup report leaked a credential or URL query")
	}
	if len(raw) > 3*Limit {
		t.Fatalf("startup report grew to %d bytes", len(raw))
	}
	report, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "failed" || report.Kind != "browser_crash" {
		t.Fatalf("report=%+v", report)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", fi.Mode())
	}
}

func TestReportDropsOutputAtCDPBoundaryAndAfterReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.json")
	r, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.Write([]byte("startup only\n"))
	r.Phase("browser_connected")
	_, _ = r.Write([]byte("page contents and keyboard"))
	r.Phase("initializing_renderer")
	r.Fail(errors.New("graphics initialization failed"), 1)
	report, _ := Read(path)
	if report.Output != "" || report.Kind != "terminal_initialization" {
		t.Fatalf("post-CDP output was retained: %+v", report)
	}
	r2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	r2.Phase("ready")
	r2.Fail(errors.New("private runtime error"), 1)
	report, _ = Read(path)
	if report.State != "ready" || report.Error != "" {
		t.Fatal("startup record captured runtime content")
	}
}

func TestStartupFailureCategories(t *testing.T) {
	for _, tc := range []struct{ phase, text, kind string }{
		{"starting_chromium", "exec: chromium: executable file not found in PATH", "browser_not_installed"},
		{"starting_chromium", "sandbox initialization failed: Operation not permitted", "runtime_permission"},
		{"initializing_terminal", "no terminal on stdin", "terminal_initialization"},
		{"starting_chromium", "Received signal 11", "browser_crash"},
	} {
		if got := FailureKind(tc.phase, tc.text); got != tc.kind {
			t.Fatalf("%q=%s, want %s", tc.text, got, tc.kind)
		}
	}
}
