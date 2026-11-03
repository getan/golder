package provider

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSaveCredentialAtomicPrivateAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", ".credentials.yaml")
	if err := SaveCredential(path, "openai-default", "sk-one"); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredential(path, "anthropic-default", "sk-two"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte(credentialEnvelopeMagic)) || bytes.Contains(raw, []byte("sk-one")) || bytes.Contains(raw, []byte("sk-two")) {
		t.Fatalf("credential file is not an encrypted envelope: %q", raw)
	}
	keyInfo, err := os.Stat(credentialKeyPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := keyInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("key mode = %o, want 600", got)
	}
	if key, err := os.ReadFile(credentialKeyPath(path)); err != nil || len(key) != credentialKeyBytes {
		t.Fatalf("key bytes = %d, err %v", len(key), err)
	}
	if value, err := ResolveCredentialReference(path, "anthropic-default"); err != nil || value != "sk-two" {
		t.Fatalf("resolved value = %q, err %v", value, err)
	}
	names, err := CredentialNames(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"anthropic-default", "openai-default"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if err := DeleteCredential(path, "openai-default"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCredentialReference(path, "openai-default"); err == nil {
		t.Fatal("deleted credential still resolves")
	}
}

func TestSaveCredentialMigratesLegacyPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".credentials.yaml")
	if err := os.WriteFile(path, []byte("legacy: old-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredential(path, "new", "new-secret"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte(credentialEnvelopeMagic)) || bytes.Contains(raw, []byte("old-secret")) || bytes.Contains(raw, []byte("new-secret")) {
		t.Fatalf("migration left plaintext: %q", raw)
	}
	for name, want := range map[string]string{"legacy": "old-secret", "new": "new-secret"} {
		if got, err := ResolveCredentialReference(path, name); err != nil || got != want {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestEncryptedCredentialRejectsTamperingAndMissingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".credentials.yaml")
	if err := SaveCredential(path, "api", "secret"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw[len(credentialEnvelopeMagic):])))
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 1
	raw = []byte(credentialEnvelopeMagic + base64.StdEncoding.EncodeToString(payload) + "\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCredentialReference(path, "api"); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("tampered credential error = %v", err)
	}
	if err := SaveCredential(path, "api", "secret"); err == nil {
		// The authenticated read must fail before an update can overwrite damage.
		t.Fatal("save replaced a tampered credential file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredential(path, "api", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(credentialKeyPath(path)); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCredentialReference(path, "api"); err == nil || !strings.Contains(err.Error(), "credential key") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestSaveCredentialRejectsInvalidInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".credentials.yaml")
	for _, tc := range []struct{ name, value string }{{"", "secret"}, {"bad\nname", "secret"}, {"ok", ""}} {
		if err := SaveCredential(path, tc.name, tc.value); err == nil {
			t.Fatalf("SaveCredential(%q) accepted invalid input", tc.name)
		}
	}
}
