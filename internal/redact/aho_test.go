package redact

import "testing"

// TestAhoOutputsAreUniqueAndBounded is the structural guard for the
// case-insensitive trie: one node must never carry the same rule twice, and no
// node can carry more rules than exist. The failure it guards against was a
// node processed once per case edge whose failure outputs were appended again
// each time, which turned 261 nodes into 37,934 output entries.
func TestAhoOutputsAreUniqueAndBounded(t *testing.T) {
	r := testRedactor(t, Redact)
	if got := len(r.aho.nodes); got > 4096 {
		t.Fatalf("automaton grew to %d nodes", got)
	}
	total := 0
	for i := range r.aho.nodes {
		seen := map[int]struct{}{}
		for _, ri := range r.aho.nodes[i].out {
			if _, dup := seen[ri]; dup {
				t.Fatalf("node %d repeats rule %q (%d outputs for %d rules)",
					i, r.rules[ri].id, len(r.aho.nodes[i].out), len(r.rules))
			}
			seen[ri] = struct{}{}
		}
		if len(r.aho.nodes[i].out) > len(r.rules) {
			t.Fatalf("node %d has %d outputs for %d rules", i, len(r.aho.nodes[i].out), len(r.rules))
		}
		total += len(r.aho.nodes[i].out)
	}
	if total > len(r.aho.nodes)*len(r.rules) {
		t.Fatalf("%d output entries for %d nodes and %d rules", total, len(r.aho.nodes), len(r.rules))
	}
	t.Logf("nodes=%d outputs=%d rules=%d", len(r.aho.nodes), total, len(r.rules))
}

func TestAhoCaseInsensitiveAnchors(t *testing.T) {
	r := testRedactor(t, Redact)
	res := r.Scan(SeamToolOutput, "test", "sk-ant-"+repeat("a", 32))
	if len(res.Findings) == 0 || res.Findings[0].Rule != "anthropic_key" {
		t.Fatalf("lowercase key findings = %+v", res.Findings)
	}
	// The generic rule's regex is (?i); the anchor must reach it in caps.
	res = r.Scan(SeamToolOutput, "test", "PASSWORD = \""+genericPass+"\"")
	if len(res.Findings) == 0 || res.Findings[0].Rule != "generic_assignment" {
		t.Fatalf("caps assignment findings = %+v", res.Findings)
	}
}

func BenchmarkScanDenseAnchors(b *testing.B) {
	text := repeat(`password = "correcthorsebattery"
token token token api_key secret sk-ant-`+repeat("a", 32)+`
`, 3000)
	r, err := New(Config{Mode: Redact})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Scan(SeamToolOutput, "bench", text)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
