package mermaidwasm

import (
	"context"
	"os"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

func TestValidateDiagram(t *testing.T) {
	for _, source := range []string{
		"flowchart TD\nA-->B", "graph LR\nA-->B", "sequenceDiagram\nA->>B: hello",
		"classDiagram\nA <|-- B", "stateDiagram-v2\nA --> B", "erDiagram\nA ||--o{ B : has",
	} {
		if err := validateDiagram(source); err != nil {
			t.Errorf("%q: %v", source, err)
		}
	}
	for _, source := range []string{"", "venn-beta\nA", "---\ntitle: hi\n---\nflowchart TD\nA-->B", "%%{init: {}}%%\nflowchart TD\nA-->B", "graph\x00 LR"} {
		if err := validateDiagram(source); err == nil {
			t.Errorf("unsupported input accepted: %q", source)
		}
	}
}

func TestRejectUntrustedModule(t *testing.T) {
	if _, err := Render(context.Background(), []byte("not a module"), "flowchart TD\nA-->B"); err == nil {
		t.Fatal("untrusted WASM accepted")
	}
}

// Opt-in integration test uses the independently downloaded pinned upstream
// artifact. It never downloads anything at test or application runtime.
func TestPinnedUpstreamModule(t *testing.T) {
	path := os.Getenv("GOLDER_TEST_MERMAID_WASM")
	if path == "" {
		t.Skip("set GOLDER_TEST_MERMAID_WASM to the pinned upstream module")
	}
	module, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		"flowchart TD\nA[Start] --> B[Finish]",
		"sequenceDiagram\nAlice->>Bob: Hello\nBob-->>Alice: Done",
		"classDiagram\nAnimal <|-- Dog",
		"stateDiagram-v2\n[*] --> Idle\nIdle --> [*]",
		"erDiagram\nCUSTOMER ||--o{ ORDER : places",
	} {
		svg, err := Render(context.Background(), module, source)
		if err != nil || !strings.Contains(svg, "<svg") {
			t.Fatalf("%q: svg length %d, error %v", source, len(svg), err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, module, "flowchart TD\nA-->B"); err == nil {
		t.Fatal("canceled request succeeded")
	}
	if _, err := Render(context.Background(), module, "flowchart TD\n"+strings.Repeat("A", maxInputBytes)); err == nil {
		t.Fatal("oversized input succeeded")
	}
	if svg, err := RenderWithFont(context.Background(), module, "flowchart LR\nA-->B", goregular.TTF); err != nil || !strings.Contains(svg, "<svg") {
		t.Fatalf("font-backed Mermaid render failed: %v", err)
	}
}
