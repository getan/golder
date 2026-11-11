package patch

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestInlineFragmentDiagnosticIsBoundedAndDoesNotApply(t *testing.T) {
	dir := t.TempDir()
	line := `return <Card data-label="` + strings.Repeat("保", 900) + `"><Button onClick={()=>void save(true)}>保存最后一条</Button></Card>;`
	writeFile(t, dir, "a.tsx", strings.Repeat("// preceding\n", 99)+line+"\n")
	changes, err := computeFor(t, dir, "*** Begin Patch\n*** Update File: a.tsx\n@@\n onClick={()=>void save(true)}\n-保存最后一条\n+已保存\n*** End Patch\n")
	if err == nil || changes != nil {
		t.Fatal("inline fragments must not become an implicit substring edit")
	}
	message := err.Error()
	for _, want := range []string{"closest line is 100", fmt.Sprintf("%d bytes", len(line)), "inline fragment", "matches whole lines", "complete -/+ line", "max_line_bytes", "preview only", "nothing was written"} {
		if !strings.Contains(message, want) {
			t.Fatalf("diagnostic missing %q: %s", want, message)
		}
	}
	if len(message) > 1800 || !utf8.ValidString(message) || strings.Contains(message, "Re-read the file and resend") {
		t.Fatalf("oversized, invalid or unhelpful diagnostic (%d bytes): %s", len(message), message)
	}
}

func TestFormattedFragmentsOfSingleLineGetActionableDiagnostic(t *testing.T) {
	message := hunkDiagnostic([]string{`return <Card><Button onClick={()=>void save(true)}>保存</Button></Card>;`}, []string{"return (", "<Card>", "<Button onClick={()=>void save(true)}>", "保存", "</Button>", "</Card>", ");"})
	if !strings.Contains(message, "inline fragment") || !strings.Contains(message, "matches whole lines") {
		t.Fatalf("formatted context longer than the file lost the fragment hint: %s", message)
	}
}

func TestEveryDiagnosticRepresentationOfLongLinesIsBounded(t *testing.T) {
	for _, text := range []string{
		strings.Repeat(`\"`, 20_000),
		strings.Repeat("保存\x01\t", 10_000),
	} {
		t.Run(fmt.Sprintf("%d", len(text)), func(t *testing.T) {
			preview := diagnosticPreview(text)
			if len(preview) > diagnosticLineBytes+80 || !utf8.ValidString(preview) || !strings.Contains(preview, fmt.Sprintf("%d bytes total", len(text))) {
				t.Fatalf("escaped preview is not bounded: %d bytes", len(preview))
			}
			// One real context line matches, so this exercises expected/found and
			// the surrounding block, not just the no-context closest-line case.
			message := hunkDiagnostic([]string{"anchor", text + " actual"}, []string{"anchor", text + " expected"})
			if len(message) > diagnosticMaxBytes || !utf8.ValidString(message) || !strings.Contains(message, "closest match") || !strings.Contains(message, "preview only") {
				t.Fatalf("mismatch block is not bounded: %d bytes", len(message))
			}
		})
	}
}

func TestUnrelatedLongLineDoesNotRecommendIdenticalReread(t *testing.T) {
	line := "const key = " + strings.Repeat("x", 28_040)
	message := hunkDiagnostic([]string{line}, []string{"const key = stale()"})
	if len(message) > 1400 || !strings.Contains(message, "28052-byte source line") || !strings.Contains(message, "raise max_line_bytes") || !strings.Contains(message, "split/format") {
		t.Fatalf("long closest line recovery is not actionable: %s", message)
	}
}
