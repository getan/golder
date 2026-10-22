package judge

import "testing"

// NetworkKey names the host a network question is about, for the dialog. The
// network itself is decided per call; a tool whose target this layer cannot
// name simply renders as "network access".
func TestNetworkKey(t *testing.T) {
	cases := []struct {
		tool string
		args string
		key  string
		ok   bool
	}{
		{"webfetch", `{"url":"https://api.github.com/repos"}`, "api.github.com", true},
		{"webfetch", `{"url":"http://127.0.0.1/x"}`, "127.0.0.1", true},
		{"webfetch", `{"url":"https://example.com"}`, "example.com", true},
		{"webfetch", `{"url":"not a url"}`, "", false},
		{"websearch", `{"query":"go"}`, "websearch", true},
		{"bash", `{"command":"git push origin main"}`, "", false},
	}
	for _, c := range cases {
		key, ok := NetworkKey(c.tool, []byte(c.args))
		if ok != c.ok || key != c.key {
			t.Errorf("%s %s = (%q,%v), want (%q,%v)", c.tool, c.args, key, ok, c.key, c.ok)
		}
	}
}
