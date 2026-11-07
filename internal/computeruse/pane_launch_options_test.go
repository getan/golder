package computeruse

import (
	"slices"
	"testing"
)

func TestOperatorPaneIsFocusedAndClosesWithHelper(t *testing.T) {
	got := operatorPaneParams("pane", "/project", "/golder", "/tmp/owner.sock")
	if !got.Focus || !got.CloseOnExit || got.Name != "pane" || got.Cwd != "/project" || !slices.Equal(got.Command, []string{"/golder", "computer-pane", "/tmp/owner.sock"}) {
		t.Fatalf("operator pane parameters = %+v", got)
	}
}
