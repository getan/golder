package judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

// The user's failure shape: a reviewed sandbox escape with a network need.
// The capability suffix and the LLM explanation must both survive, in both
// automatic decisions and the note emitted after a human's approval.
func TestConsentCapabilityApprovalKeepsReviewerRationale(t *testing.T) {
	for _, mode := range []permissions.Mode{permissions.Auto, permissions.Ask} {
		for _, tc := range []struct{ lang, reason, capability string }{
			{"zh", "仅运行隔离临时 HOME 下的 Go 回归测试，不访问个人配置。", "已授权：联网"},
			{"en", "Runs Go regression fixtures with an isolated temporary HOME, without personal configuration.", "granted: network"},
		} {
			t.Run(mode.String()+"/"+tc.lang, func(t *testing.T) {
				grantstest.Install(t)
				var notes []Note
				gate := ConsentGate(ConsentOpts{
					Mode: permissions.New(mode), Cwd: t.TempDir(),
					Classifier: consentClassifier{v: Verdict{Level: Confirm, Source: "llm", Reasons: []string{tc.reason}, Lang: tc.lang}},
					Notify:     func(n Note) { notes = append(notes, n) },
					Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
						if req.Rationale != tc.reason {
							t.Errorf("prompt lost rationale: %q", req.Rationale)
						}
						return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce, Exec: ExecUnconfined, Needs: req.Needs}
					},
				})
				ctx := agentcore.WithMessageSnapshot(context.Background(), nil)
				dec := gate(ctx, agentcore.AgentToolCall{ID: "reviewed-tests", Name: "bash", Arguments: json.RawMessage(`{"command":"go test ./...","network":"on","sandbox_permissions":"require_escalated","justification":"run isolated local tests"}`)})
				if dec == nil || dec.Block || !dec.Direct || !dec.Network {
					t.Fatalf("execution authorization changed: %+v", dec)
				}
				if len(notes) != 1 {
					t.Fatalf("notes: %+v", notes)
				}
				if notes[0].Rationale != tc.reason {
					t.Fatalf("review rationale replaced: %q", notes[0].Rationale)
				}
				got := FormatNote(notes[0])
				if !strings.Contains(got, tc.reason) || !strings.Contains(got, "via llm") {
					t.Fatalf("review explanation missing: %s", got)
				}
				// Note language follows the conversation; this unit has no text
				// conversation, so verify its two languages explicitly too.
				note := notes[0]
				note.Lang = tc.lang
				got = FormatNote(note)
				if strings.Count(got, tc.capability) != 1 || strings.Count(got, tc.reason) != 1 {
					t.Fatalf("rationale/capabilities duplicated or missing: %s", got)
				}
			})
		}
	}
}

func TestConsentRationaleUsesFirstNonblankReason(t *testing.T) {
	if got := consentRationale(Verdict{Reasons: []string{" ", "keeps the test fixtures isolated"}}, []Need{{Cap: CapNetwork}}, "en"); got != "keeps the test fixtures isolated" {
		t.Fatal(got)
	}
}

func TestFormatNoteKeepsProseBeginningWithGranted(t *testing.T) {
	for _, tc := range []struct{ lang, reason string }{
		{"zh", "已授权：联网，仅用于回环测试服务，不访问个人配置。"},
		{"en", "granted: network, solely for loopback fixtures without personal configuration."},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			n := Note{Tool: "bash", Kind: NoteEscaped, Mode: "auto", Level: Confirm, Source: "llm", Lang: tc.lang, Granted: []Need{{Cap: CapNetwork}}, Rationale: tc.reason}
			if got := FormatNote(n); !strings.HasSuffix(got, ": "+tc.reason) {
				t.Fatalf("prose mistaken for a duplicate capability list: %s", got)
			}
		})
	}
}
