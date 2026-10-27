package workspace

import (
	"slices"
	"testing"
)

func TestWorkspacePaneGrantsDefaultAndOverride(t *testing.T) {
	t.Setenv("GOLDER_WORKSPACE_PANE_GRANTS", "")
	if got := workspacePaneGrants(); !slices.Equal(got, []string{"read", "write", "fan"}) {
		t.Fatalf("default grants = %v", got)
	}

	t.Setenv("GOLDER_WORKSPACE_PANE_GRANTS", " read , admin ,, ")
	if got := workspacePaneGrants(); !slices.Equal(got, []string{"read", "admin"}) {
		t.Fatalf("override grants = %v", got)
	}

	t.Setenv("GOLDER_WORKSPACE_PANE_GRANTS", " , ")
	if got := workspacePaneGrants(); !slices.Equal(got, []string{"read", "write", "fan"}) {
		t.Fatalf("blank override must keep the strict default, got %v", got)
	}
}
