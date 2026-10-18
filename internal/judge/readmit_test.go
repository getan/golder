package judge

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"
)

// The stdin shape of the denial question: the same three answers the dialog
// offers, including the one that runs this command outside the sandbox.
func TestAskReAdmitAnswers(t *testing.T) {
	const path = "/Volumes/KIOXIA/cache"
	cases := []struct {
		name       string
		line       string
		approve    bool
		unconfined bool
		readRoot   string
	}{
		{"once", "y\n", true, false, ""},
		{"unconfined", "e\n", true, true, ""},
		{"unconfined word", "escape\n", true, true, ""},
		{"skip", "\n", false, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			ans := AskReAdmit(context.Background(), bufio.NewReader(strings.NewReader(c.line)),
				&out, nil, path, "cat "+path, "en")
			if !ans.Answered {
				t.Fatal("an answer was given but Answered is false")
			}
			if ans.Approve != c.approve || ans.Unconfined != c.unconfined {
				t.Fatalf("answer = %+v, want approve=%v unconfined=%v", ans, c.approve, c.unconfined)
			}
			if !strings.Contains(out.String(), "outside the sandbox") {
				t.Errorf("prompt = %q, want the unconfined option described", out.String())
			}
		})
	}
}
