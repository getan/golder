package redact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewTextUsesPolicyWithoutAuditOrRestoreTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	r, err := New(Config{Mode: Redact, AuditPath: path})
	if err != nil {
		t.Fatal(err)
	}
	secret := "fixture-known-secret-12345"
	r.AddKnown(secret)
	for range 3 {
		if got := r.PreviewText("edit", "before "+secret+" after"); got != "before [REDACTED] after" {
			t.Fatalf("preview = %q", got)
		}
	}
	if len(r.tokens) != 0 {
		t.Fatal("preview created reversible restore tokens")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 0 {
		t.Fatalf("preview wrote an audit entry: %q %v", data, err)
	}
	if got := r.Text(SeamToolOutput, "edit", secret); strings.Contains(got, secret) || !strings.Contains(got, "«SECRET:") {
		t.Fatalf("audited scan changed: %q", got)
	}
	data, err = os.ReadFile(path)
	if err != nil || len(data) == 0 || strings.Contains(string(data), secret) {
		t.Fatalf("audited scan did not record safely: %q %v", data, err)
	}
}

func TestPreviewTextHonorsModesAndNewKnownSecrets(t *testing.T) {
	secret := "fixture-known-secret-67890"
	for _, mode := range []Mode{Off, Observe, Redact, Block} {
		r := testRedactor(t, mode)
		r.AddKnown(secret)
		got := r.PreviewText("read", secret)
		switch mode {
		case Off, Observe:
			if got != secret {
				t.Fatalf("mode %v: %q", mode, got)
			}
		case Redact:
			if got != "[REDACTED]" {
				t.Fatalf("mode %v: %q", mode, got)
			}
		case Block:
			if strings.Contains(got, secret) || !strings.Contains(got, "blocked by redact") {
				t.Fatalf("mode %v: %q", mode, got)
			}
		}
	}
	r := testRedactor(t, Redact)
	if got := r.PreviewText("read", secret); got != secret {
		t.Fatalf("unexpected initial preview: %q", got)
	}
	r.AddKnown(secret)
	if got := r.PreviewText("read", secret); got != "[REDACTED]" {
		t.Fatalf("new known secret was not applied to the next preview: %q", got)
	}
}
