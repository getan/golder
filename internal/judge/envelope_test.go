package judge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/permissions"
)

func TestEnvelopeBare(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	secret := filepath.Join(home, ".ssh", "id_ed25519")
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{"git push", "git push origin master", true},
		{"read", "ls -la internal", true},
		{"project test", "go test ./...", true},
		{"credential", "cat " + secret, false},
		{"shell", "bash -c 'echo hi'", false},
		{"interpreter", "python3 -c 'print(1)'", false},
		{"make", "make test", false},
		{"wrapper", "env FOO=bar ls", false},
		{"variable", "cat $HOME/notes.txt", false},
		{"path assignment", "PATH=/tmp/bin ls", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := EnvelopeBare(tc.cmd, "en")
			if got != tc.want {
				t.Fatalf("EnvelopeBare(%q) = %v (%s), want %v", tc.cmd, got, why, tc.want)
			}
			if why == "" {
				t.Error("the envelope should always give a reason")
			}
		})
	}
}

// TestAutoApprovalCarriesTheBareEnvelope drives the seam: an enumerable allow
// carries the envelope on the decision, and a command that runs text this layer
// cannot read keeps the default (contained) shape.
func TestAutoApprovalCarriesTheBareEnvelope(t *testing.T) {
	gate := PermissionGate(permissions.New(permissions.Auto), GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Allow}},
		Sandboxed:  func(string) bool { return true },
	})

	dec := gate(context.Background(), toolCall("bash", `{"command":"git push origin master"}`))
	if dec == nil || !dec.Bare || dec.Sandbox || dec.Block {
		t.Fatalf("an enumerable allow must carry the bare envelope, got %+v", dec)
	}

	dec = gate(context.Background(), toolCall("bash", `{"command":"bash -c 'echo hi'"}`))
	if dec != nil {
		t.Fatalf("a shell must stay contained, got %+v", dec)
	}
}
