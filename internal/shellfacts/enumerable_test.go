package shellfacts

import (
	"strings"
	"testing"
)

func TestEnumerable(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
		why  string // required substring when want is false
	}{
		{"read", "ls -la internal", true, ""},
		{"push", "git push origin master", true, ""},
		{"glob", "rg -n TODO src/*.go", true, ""},
		{"tilde", "cat ~/notes.txt", true, ""},
		{"plain assignment", "FOO=bar ls", true, ""},
		{"literal redirect", "echo x > out.txt", true, ""},
		{"cache assignment", "GOCACHE=/Volumes/cache go test ./...", true, ""},
		{"variable", "cat $HOME/notes.txt", false, "expands a variable"},
		{"command substitution", "cat $(echo /etc/passwd)", false, "command substitution"},
		{"process substitution", "diff <(ls a) <(ls b)", false, "process substitution"},
		{"dynamic redirect", "echo x > $OUT", false, "expands a variable"},
		{"path assignment", "PATH=/tmp/bin ls", false, "changes what runs"},
		{"home assignment", "HOME=/tmp go test ./...", false, "changes what runs"},
		{"preload assignment", "LD_PRELOAD=/tmp/x.so ls", false, "changes what runs"},
		{"for loop", "for f in *.go; do echo $f; done", false, ""},
		{"arithmetic", "echo $((1+1))", false, "arithmetic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := Parse(tc.cmd).Enumerable()
			if got != tc.want {
				t.Fatalf("Enumerable(%q) = %v (%s), want %v", tc.cmd, got, why, tc.want)
			}
			if tc.want {
				if why != "" {
					t.Errorf("an enumerable plan should not give a reason, got %q", why)
				}
				return
			}
			if why == "" {
				t.Fatalf("Enumerable(%q) refused without a reason", tc.cmd)
			}
			if tc.why != "" && !strings.Contains(why, tc.why) {
				t.Errorf("reason = %q, want it to mention %q", why, tc.why)
			}
		})
	}
}
