package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetProviderDefaultsPreservesCommentsAndRemovesLiteralKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	original := "# keep me\napi_key = \"legacy-secret\"\nfoo = \"bar\"\n\n[memory]\nenabled = false\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	err := SetProviderDefaults(ProviderDefaults{
		Provider: "openai", Model: "gpt-test", BaseURL: "https://api.example/v1",
		Protocol: "responses", Credential: "openai-default", ThinkingLevel: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"# keep me", `foo = "bar"`, "[memory]", "enabled = false", `provider = "openai"`, `credential = "openai-default"`} {
		if !strings.Contains(text, want) {
			t.Errorf("config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "legacy-secret") || strings.Contains(text, "api_key") {
		t.Fatalf("literal api key survived:\n%s", text)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
}
