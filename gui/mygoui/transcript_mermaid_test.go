package mygoui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func TestSimpleMermaid(t *testing.T) {
	valid := `flowchart LR
A[开始] --> B[处理]
B --> C[结束]`
	graph := parseSimpleMermaid(valid)
	if graph == nil || !graph.horizontal || len(graph.nodes) != 3 || graph.nodes[1].label != "处理" {
		t.Fatalf("unexpected graph: %+v", graph)
	}
	vertical := parseSimpleMermaid("graph TD\nA --> B\nB --> C\n")
	if vertical == nil || vertical.horizontal || len(vertical.nodes) != 3 {
		t.Fatalf("unexpected vertical graph: %+v", vertical)
	}
	branch := parseSimpleMermaid("flowchart TD\nA --> B\nA --> C\nB --> D")
	if branch == nil || len(branch.nodes) != 4 || len(branch.edges) != 3 {
		t.Fatalf("unexpected tree: %+v", branch)
	}
	chain := parseSimpleMermaid("flowchart LR\nA --> B --> C;")
	if chain == nil || len(chain.nodes) != 3 || len(chain.edges) != 2 {
		t.Fatalf("unexpected chain: %+v", chain)
	}
	decision := parseSimpleMermaid("flowchart TD\nA[Start] --> B{Ready}\nB -->|Yes| C(Done)\nB -->|No| D[Retry]")
	if decision == nil || decision.nodes[1].shape != "diamond" || decision.nodes[2].shape != "round" || decision.edges[1].label != "Yes" {
		t.Fatalf("unexpected decision tree: %+v", decision)
	}
	for _, src := range []string{
		"sequenceDiagram\nA->>B: Hello",
		"flowchart TD\nA --> B\nA --> C\nB --> D\nC --> D",
		"flowchart TD\nA --> B\nB --> A",
		"flowchart TD\nA -->|<b>yes</b>| B",
		"flowchart TD\nA --> B\nA --> B",
		"flowchart TD\nA --> B\nclassDef a fill:red",
		"%%{init: {}}%%\nflowchart LR\nA --> B",
		"flowchart LR\nA[<b>unsafe</b>] --> B",
		"flowchart LR\nA[\"escaped\\\"quoted\"] --> B",
		"flowchart LR\nA[" + strings.Repeat("a", 25) + "] --> B",
	} {
		if g := parseSimpleMermaid(src); g != nil {
			t.Errorf("unsupported graph rendered: %q => %+v", src, g)
		}
	}
}

func TestMermaidPreviewAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
	}{
		{"linear", "flowchart LR\nA[Start] --> B[End]", "Mermaid flowchart preview"},
		{"branch", "flowchart TD\nA{Start} -->|Yes| B(One)\nA -->|No| C[Two]", "Mermaid flowchart preview"},
		{"sequence", "sequenceDiagram\nA->>B: Hello\nB-->>A: Done", "Mermaid sequence preview"},
		{"unsupported", "sequenceDiagram\nloop forever\nA->>B: Hello\nend", "Mermaid preview unavailable; showing source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			item := appsession.Item{ID: "mermaid", Kind: appsession.ItemAssistant, Done: true, Text: "```mermaid\n" + tc.source + "\n```"}
			tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 800, 400)
			if _, ok := tt.Find(tc.want); !ok {
				t.Fatalf("missing %q: %q", tc.want, tt.Texts())
			}
			if !tt.HasText("Copy") {
				t.Fatalf("missing source copy: %q", tt.Texts())
			}
			if tt.Image() == nil {
				t.Fatal("diagram did not draw")
			}
		})
	}
}

func TestSimpleMermaidSequence(t *testing.T) {
	src := "sequenceDiagram\nparticipant Client\nparticipant Server\nClient->>Server: Request\nServer-->>Client: 完成"
	seq := parseSimpleMermaidSequence(src)
	if seq == nil || len(seq.participants) != 2 || len(seq.messages) != 2 || !seq.messages[1].dashed || seq.messages[1].text != "完成" {
		t.Fatalf("unexpected sequence: %+v", seq)
	}
	for _, src := range []string{
		"sequenceDiagram\nA->>A: Self",
		"sequenceDiagram\nA->>B: Hi\nparticipant C",
		"sequenceDiagram\nparticipant A as Alice\nA->>B: Hi",
		"sequenceDiagram\nA->>B: <b>Hi</b>",
		"sequenceDiagram\nA->>B: Hi\nloop test\nend",
		"%%{init: {}}%%\nsequenceDiagram\nA->>B: Hi",
	} {
		if seq := parseSimpleMermaidSequence(src); seq != nil {
			t.Errorf("unsupported sequence rendered: %q => %+v", src, seq)
		}
	}
}
