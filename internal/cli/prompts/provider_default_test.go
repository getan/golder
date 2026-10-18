package prompts

import (
	"strings"
	"testing"
)

func TestProviderDefaultText(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())

	if got := providerDefaultText("", nil); !strings.Contains(got, "unset") {
		t.Fatalf("unset default = %q", got)
	}
	got := SetDefaultProviderText("commandcode", nil)
	if !strings.Contains(got, "default provider set to commandcode") {
		t.Fatalf("set = %q", got)
	}
	if !strings.Contains(got, "switches this one now") {
		t.Errorf("set should say what happens to this session: %q", got)
	}
	if DefaultProviderName() != "commandcode" {
		t.Fatalf("DefaultProviderName = %q", DefaultProviderName())
	}
	if got := providerDefaultText("", nil); !strings.Contains(got, "commandcode") {
		t.Fatalf("show after set = %q", got)
	}
	if got := providerDefaultText("off", nil); !strings.Contains(got, "cleared") {
		t.Fatalf("off = %q", got)
	}
	if DefaultProviderName() != "" {
		t.Fatalf("DefaultProviderName after off = %q", DefaultProviderName())
	}
	if got := SetDefaultProviderText("nope", nil); !strings.Contains(got, "unknown provider") {
		t.Errorf("unknown = %q", got)
	}
	if got := SetDefaultProviderText("typesafe", nil); !strings.Contains(got, "decision model") {
		t.Errorf("decision model = %q", got)
	}
}
