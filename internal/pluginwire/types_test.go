package pluginwire

import (
	"encoding/json"
	"testing"
)

func TestManifestKeepsLegacyCatalogMetadata(t *testing.T) {
	var got Manifest
	raw := []byte(`{"name":"audit","version":"1","tools":[{"name":"scan","description":"Scan","schema":{"type":"object"}}],"commands":[{"name":"report","description":"Report"}],"events":["turn_end"]}`)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "audit" || len(got.Tools) != 1 || len(got.Commands) != 1 || len(got.Events) != 1 {
		t.Fatalf("legacy manifest metadata changed: %+v", got)
	}
}

func TestCallResultWireShape(t *testing.T) {
	raw, err := json.Marshal(CallResult{Content: "done", IsError: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"content":"done","isError":true}` {
		t.Fatalf("wire result = %s", raw)
	}
}
