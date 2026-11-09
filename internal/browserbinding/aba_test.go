package browserbinding

import "testing"

func TestLateCompletionCannotClearReselectedEndpoint(t *testing.T) {
	b := New("conversation")
	one := Endpoint{Socket: "/tmp/one.sock", BrowserID: "one"}
	two := Endpoint{Socket: "/tmp/two.sock", BrowserID: "two"}
	b.Select(one)
	old := b.Snapshot()
	b.Select(two)
	b.Select(one)
	current := b.Snapshot()
	b.Clear(old)
	b.Present(old)
	if got := b.Snapshot(); got.Revision != current.Revision || got.Endpoint != current.Endpoint || got.Reveal != current.Reveal {
		t.Fatalf("old completion crossed an A-B-A selection: %+v", got)
	}
}
