package extensioncmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpDoesNotReadConfigurationOrStartSources(t *testing.T) {
	t.Setenv("GOLDER_HOME", "/unreadable/golder-state")
	for _, kind := range []string{"mcp", "plugin", "extensions"} {
		var out, errs bytes.Buffer
		if code := Run(kind, []string{"--help"}, strings.NewReader(""), &out, &errs); code != 0 {
			t.Fatal(kind, code, errs.String())
		}
		if !strings.Contains(out.String(), "Usage: golder "+kind) || errs.Len() != 0 {
			t.Fatal(out.String(), errs.String())
		}
	}
}
