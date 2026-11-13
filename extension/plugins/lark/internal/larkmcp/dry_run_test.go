package larkmcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestLongTailDryRunDoesNotClaimSubmission(t *testing.T) {
	for _, tc := range []struct {
		name, flag, state, verification string
	}{
		{"true", "--dry-run", "not_executed", "preview"},
		{"explicit true", "--dry-run=true", "not_executed", "preview"},
		{"false", "--dry-run=false", "submitted", "receipt_only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{}
			p := New(Config{}, f)
			args, _ := json.Marshal(map[string]any{"service": "lark", "argv": []string{"sheets", "+sheet-delete", "--sheet-id=s1", tc.flag}, "confirm": true})
			r := p.Call(approved(), "cli_call", args)
			if !r.OK || r.ExecutionState != tc.state || r.Verification != tc.verification || len(f.calls) != 1 {
				t.Fatalf("result=%+v calls=%v", r, f.calls)
			}
		})
	}
}

func TestDryRunFailureIsNotInventedAsSuccessfulPreview(t *testing.T) {
	f := &fakeRunner{fn: func(string, []string, []byte) (Output, error) {
		return Output{}, context.DeadlineExceeded
	}}
	p := New(Config{}, f)
	r := p.Call(approved(), "cli_call", json.RawMessage(`{"service":"lark","argv":["sheets","+sheet-delete","--dry-run"],"confirm":true}`))
	if r.OK || r.Verification == "preview" || r.ExecutionState != "unknown" || len(f.calls) != 1 {
		t.Fatalf("failure was misreported: %+v", r)
	}
}

func TestDryRunRejectsAmbiguousAndContradictoryFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--dry-run=true", "--dry-run=false"}, {"--dry-run=false", "--dry-run"},
		{"--dry-run", "false"}, {"--dry-run=true", "--dry-run=true"},
	} {
		if larkDryRun(args) {
			t.Fatalf("ambiguous flags were reported as preview: %v", args)
		}
	}
}
