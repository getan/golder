package agenttool

import "testing"

func TestSearchEnvOverrideFallsBackToEnvironment(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	SetSearchEnv(func(name string) string {
		if name == "DEEPSEEK_API_KEY" {
			return "gui-key"
		}
		return ""
	})
	t.Cleanup(func() { SetSearchEnv(nil) })
	if got := defaultSearchEnv("DEEPSEEK_API_KEY"); got != "gui-key" {
		t.Fatalf("override = %q, want gui-key", got)
	}
	SetSearchEnv(func(string) string { return "" })
	if got := defaultSearchEnv("DEEPSEEK_API_KEY"); got != "env-key" {
		t.Fatalf("fallback = %q, want env-key", got)
	}
}
