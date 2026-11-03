package provider

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestSaveCredentialRejectsInvalidInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".credentials.yaml")
	for _, tc := range []struct{ name, value string }{{"", "secret"}, {"bad\nname", "secret"}, {"ok", ""}} {
		if err := SaveCredential(path, tc.name, tc.value); err == nil {
			t.Fatalf("SaveCredential(%q) accepted invalid input", tc.name)
		}
	}
}
