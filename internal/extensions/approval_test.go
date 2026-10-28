package extensions

import "testing"

func TestSessionApprovalIsNotSavedWithCatalog(t *testing.T) {
	ref := SourceRef{Kind: "mcp", ID: "temporary"}
	m := testManager(t, ref, SourceConfig{Enabled: true, Transport: "http", URL: "https://example.invalid/mcp"}, nil)
	if err := m.ApproveSession(ref); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].Trust != "approved" {
		t.Fatal("not admitted for this session")
	}
	// Probing normally saves a catalog; saving must not remember temporary trust.
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	next, err := New(m.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.List()[0].Trust == "approved" {
		t.Fatal("session approval became persistent")
	}
}
