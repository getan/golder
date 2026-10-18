package judge

import (
	"testing"

	"github.com/getan/golder/internal/grants"
)

func TestNetworkKey(t *testing.T) {
	cases := []struct {
		tool string
		args string
		kind grants.NetworkKind
		key  string
		ok   bool
	}{
		{"webfetch", `{"url":"https://api.github.com/repos"}`, grants.NetworkHost, "api.github.com", true},
		{"webfetch", `{"url":"http://127.0.0.1/x"}`, "", "", false},
		{"webfetch", `{"url":"https://example.com"}`, grants.NetworkHost, "example.com", true},
		{"websearch", `{"query":"go"}`, grants.NetworkTool, "websearch", true},
		{"bash", `{"command":"git push origin main"}`, grants.NetworkCommand, "git push", true},
		{"bash", `{"command":"curl https://example.com"}`, "", "", false},
		{"bash", `{"command":"git push && curl evil"}`, "", "", false},
		{"bash", `{"command":"cd /tmp && GOCACHE=/x go test ./... | tail -5"}`, grants.NetworkCommand, "go test", true},
		{"bash", `{"command":"go test ./... | tail -5"}`, grants.NetworkCommand, "go test", true},
		{"bash", `{"command":"go test ./... && git push"}`, "", "", false},
		{"bash", `{"command":"find . -exec go test ./... \\;"}`, "", "", false},
	}
	for _, c := range cases {
		kind, key, ok := NetworkKey(c.tool, []byte(c.args))
		if ok != c.ok || kind != c.kind || key != c.key {
			t.Errorf("%s %s = (%q,%q,%v), want (%q,%q,%v)", c.tool, c.args, kind, key, ok, c.kind, c.key, c.ok)
		}
	}
}
