package reviewpolicy

// Tests for the routing policy. The load-bearing cases are the ones that must
// NOT decide: a cheap judge's confident answer may settle a call, but every path
// that cannot establish what will run, or that contradicts itself, escalates.

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/jev"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/shellfacts"
)

func cfg() Config { return DefaultConfig() }

// choice builds a choice answer.
func choice(option string, p map[string]float64, confidence float64) jev.Answer {
	return jev.Answer{Type: jev.AnswerChoice, Choice: option, Probabilities: p, Confidence: confidence}
}

// noul builds a yes/no answer.
func noul(p float64) jev.Answer { return jev.Answer{Type: jev.AnswerNoul, Noul: p} }

// score builds a score answer.
func score(v float64) jev.Answer {
	return jev.Answer{Type: jev.AnswerScore, Score: v, Probabilities: map[string]float64{"0": 1}}
}

// answers assembles a full answer set with a clean bill from the danger signals
// unless the caller overrides one.
func answers(level string, levelProbs map[string]float64, confidence float64, tweaks ...func(map[string]jev.Answer)) map[string]jev.Answer {
	out := map[string]jev.Answer{
		QL:   choice(level, levelProbs, confidence),
		QR:   score(1),
		QRev: noul(0.01),
		QSec: noul(0.01),
		// Intent is opt-in in these fixtures: most cases test routing without a
		// user request in play. A contain case that sets it high escalates.
		QInt: noul(0.05),
		QWhy: choice("read_only", map[string]float64{"read_only": 0.9, "unknown": 0.1}, 0.9),
	}
	for _, fn := range tweaks {
		fn(out)
	}
	return out
}

func TestDecisiveAllowIsAccepted(t *testing.T) {
	out := Decide(Request{
		Answers: answers("allow", map[string]float64{"allow": 0.97, "confirm": 0.02, "deny": 0.01}, 0.95),
		Config:  cfg(),
	})
	if out.Route != RouteDecided {
		t.Fatalf("escalated (%s), want a decided allow", out.Escalation)
	}
	if out.Level != judge.Allow {
		t.Errorf("level = %v, want allow", out.Level)
	}
	if out.Rationale == "" {
		t.Error("no rationale")
	}
	if out.Risk != "medium" {
		t.Errorf("risk = %q, want medium for a score of 1", out.Risk)
	}
}

func TestAllowOnAFactHeavyCommandStillEscalates(t *testing.T) {
	// The point of routing on facts first: a computed program name means the
	// judgement is about something other than what will run, so no confidence
	// can make it a decision.
	cases := map[string]string{
		"dynamic program": "escalates",
		"unparsable":      "escalates",
		"control flow":    "escalates",
	}
	_ = cases

	t.Run("computed program name", func(t *testing.T) {
		out := Decide(Request{
			Facts:   shellfacts.Parse(`$(echo rm) -rf /`),
			Answers: answers("allow", map[string]float64{"allow": 0.99, "deny": 0.01}, 0.99),
			Config:  cfg(),
		})
		if out.Route != RouteEscalate {
			t.Fatalf("a computed program name was decided (%v)", out.Level)
		}
		if out.Escalation != EscalateDynamic {
			t.Errorf("escalation = %q, want %q", out.Escalation, EscalateDynamic)
		}
	})

	t.Run("unparsable command", func(t *testing.T) {
		out := Decide(Request{
			Facts:   shellfacts.Parse(`ls "unterminated`),
			Answers: answers("allow", map[string]float64{"allow": 0.99, "deny": 0.01}, 0.99),
			Config:  cfg(),
		})
		if out.Route != RouteEscalate || out.Escalation != EscalateUnusableFacts {
			t.Errorf("outcome = %+v, want escalation for unusable facts", out)
		}
	})

	t.Run("control flow", func(t *testing.T) {
		out := Decide(Request{
			Facts:   shellfacts.Parse(`for f in *; do rm "$f"; done`),
			Answers: answers("allow", map[string]float64{"allow": 0.99, "deny": 0.01}, 0.99),
			Config:  cfg(),
		})
		if out.Route != RouteEscalate || out.Escalation != EscalateConstructs {
			t.Errorf("outcome = %+v, want escalation for control flow", out)
		}
	})

	t.Run("plain command with clean facts is decidable", func(t *testing.T) {
		// The control: the same answer set with parseable facts does decide,
		// so the tests above are measuring the facts and not the answers.
		out := Decide(Request{
			Facts:   shellfacts.Parse(`ls -la`),
			Answers: answers("allow", map[string]float64{"allow": 0.97, "confirm": 0.03}, 0.95),
			Config:  cfg(),
		})
		if out.Route != RouteDecided || out.Level != judge.Allow {
			t.Errorf("outcome = %+v, want a decided allow", out)
		}
	})
}

func TestDangerSignalOverridesAPermissiveLevel(t *testing.T) {
	// The independent signals exist to catch what a hedged level label hides.
	t.Run("irreversible", func(t *testing.T) {
		out := Decide(Request{
			Answers: answers("allow", map[string]float64{"allow": 0.99, "deny": 0.01}, 0.99,
				func(m map[string]jev.Answer) { m[QRev] = noul(0.9) }),
			Config: cfg(),
		})
		if out.Route != RouteEscalate {
			t.Fatalf("a confident allow survived a 0.9 irreversibility signal: %+v", out)
		}
		if out.Escalation != EscalateDanger {
			t.Errorf("escalation = %q, want %q", out.Escalation, EscalateDanger)
		}
	})

	t.Run("credential material", func(t *testing.T) {
		out := Decide(Request{
			Answers: answers("allow", map[string]float64{"allow": 0.99, "deny": 0.01}, 0.99,
				func(m map[string]jev.Answer) { m[QSec] = noul(0.7) }),
			Config: cfg(),
		})
		if out.Route != RouteEscalate {
			t.Errorf("a confident allow survived a credential signal: %+v", out)
		}
	})

	t.Run("a moderate danger signal still permits containment", func(t *testing.T) {
		// Sandbox is the safe direction, so a danger signal does not have to
		// block it — that is the difference between escalating and containing.
		out := Decide(Request{
			Answers: answers("contain", map[string]float64{"contain": 0.8, "allow": 0.1}, 0.75,
				func(m map[string]jev.Answer) { m[QRev] = noul(0.6) }),
			Config: cfg(),
		})
		if out.Route != RouteDecided || out.Level != judge.Contain {
			t.Errorf("outcome = %+v, want a decided sandbox", out)
		}
	})
}

func TestMissingDangerSignalEscalates(t *testing.T) {
	// Absence is not a clean bill. If the danger question went unanswered, the
	// policy must not read that as "no".
	as := answers("allow", map[string]float64{"allow": 0.99, "deny": 0.01}, 0.99)
	delete(as, QRev)
	out := Decide(Request{Answers: as, Config: cfg()})
	if out.Route != RouteEscalate {
		t.Errorf("a missing danger signal was treated as clean: %+v", out)
	}
}

func TestConfirmAlwaysEscalates(t *testing.T) {
	// In auto mode a Confirm runs anyway, so accepting the cheap judge's
	// Confirm as final would make it the weakest link in the chain.
	for _, confidence := range []float64{0.5, 0.9, 0.99} {
		out := Decide(Request{
			Answers: answers("confirm", map[string]float64{"confirm": 0.95, "allow": 0.05}, confidence),
			Config:  cfg(),
		})
		if out.Route != RouteEscalate || out.Escalation != EscalateAmbiguous {
			t.Errorf("confidence %v: outcome = %+v, want escalation", confidence, out)
		}
	}
}

func TestDenyAndSandboxAcceptModerateConfidence(t *testing.T) {
	// Their failure modes are cheap — a redundant block or a redundant sandbox
	// — so they are accepted where Allow would not be.
	for _, c := range []struct {
		option string
		level  judge.Level
	}{
		{"deny", judge.Deny},
		{"contain", judge.Contain},
	} {
		out := Decide(Request{
			Answers: answers(c.option, map[string]float64{c.option: 0.7, "allow": 0.3}, 0.65),
			Config:  cfg(),
		})
		if out.Route != RouteDecided {
			t.Errorf("%s at 0.65 confidence escalated (%s), want decided", c.option, out.Escalation)
			continue
		}
		if out.Level != c.level {
			t.Errorf("%s produced level %v", c.option, out.Level)
		}
	}
}

// The separate escape answer cannot survive a containment decision: contain runs
// the call inside the sandbox, so the verdict must not claim a direct grant the
// gate will ignore.
func TestContainDropsTheEscapeAnswer(t *testing.T) {
	out := Decide(Request{
		Answers: answers("contain", map[string]float64{"contain": 0.9, "allow": 0.05}, 0.85,
			func(m map[string]jev.Answer) { m[QDir] = noul(0.99) }),
		Config:          cfg(),
		DirectRequested: true,
	})
	if out.Route != RouteDecided || out.Level != judge.Contain {
		t.Fatalf("outcome = %+v, want a decided contain", out)
	}
	if out.Direct {
		t.Errorf("Direct = true next to contain; the escape must be dropped")
	}
}

// Contain is the safe default, but it is not the last word when the cheap judge
// is confident the user asked for this effect: the session reviewer sees the
// transcript and can grade a bounded, explicitly requested action as confirm.
func TestContainWithExplicitIntentEscalates(t *testing.T) {
	out := Decide(Request{
		Answers: answers("contain", map[string]float64{"contain": 0.9, "allow": 0.05}, 0.85,
			func(m map[string]jev.Answer) { m[QInt] = noul(0.9) }),
		Config: cfg(),
	})
	if out.Route != RouteEscalate {
		t.Fatalf("outcome = %+v, want escalation to the session reviewer", out)
	}
	if out.Escalation != EscalateIntent {
		t.Errorf("escalation = %q, want %q", out.Escalation, EscalateIntent)
	}
}

// The same contain verdict without an explicit request stays decided: there is
// no context for the slow reviewer to weigh, and isolation is the safe answer.
func TestContainWithoutIntentStaysDecided(t *testing.T) {
	out := Decide(Request{
		Answers: answers("contain", map[string]float64{"contain": 0.9, "allow": 0.05}, 0.85),
		Config:  cfg(),
	})
	if out.Route != RouteDecided || out.Level != judge.Contain {
		t.Fatalf("outcome = %+v, want a decided contain", out)
	}
}

func TestAllowNeedsMoreThanDenyOrSandbox(t *testing.T) {
	// The asymmetry, stated as a test: the same confidence that settles a deny
	// leaves an allow escalated.
	const conf = 0.7
	deny := Decide(Request{
		Answers: answers("deny", map[string]float64{"deny": 0.7, "allow": 0.3}, conf),
		Config:  cfg(),
	})
	allow := Decide(Request{
		Answers: answers("allow", map[string]float64{"allow": 0.7, "deny": 0.3}, conf),
		Config:  cfg(),
	})
	if deny.Route != RouteDecided {
		t.Errorf("deny escalated at %v confidence", conf)
	}
	if allow.Route != RouteEscalate {
		t.Errorf("allow was decided at %v confidence, want escalation", conf)
	}
}

func TestNarrowMarginEscalates(t *testing.T) {
	// High confidence with a close runner-up is not a verdict: 0.86/0.84 means
	// the judge could not choose, and the label on top is arbitrary.
	out := Decide(Request{
		Answers: answers("allow", map[string]float64{"allow": 0.86, "confirm": 0.13, "deny": 0.01}, 0.95),
		Config:  cfg(),
	})
	// 0.86-0.13 = 0.73, which clears AllowMargin: this one IS decisive. The
	// interesting case is a genuinely close split, below.
	if out.Route != RouteDecided {
		t.Fatalf("a wide-margin allow escalated: %s", out.Escalation)
	}

	close := Decide(Request{
		Answers: answers("allow", map[string]float64{"allow": 0.51, "confirm": 0.48, "deny": 0.01}, 0.95),
		Config:  cfg(),
	})
	if close.Route != RouteEscalate {
		t.Errorf("a 0.51/0.48 split was decided as allow: %+v", close)
	}
}

func TestInconsistentDistributionEscalates(t *testing.T) {
	// The reported choice must be the distribution's winner. A model that says
	// "allow" while the probabilities favour "deny" has contradicted itself.
	out := Decide(Request{
		Answers: answers("allow", map[string]float64{"allow": 0.2, "deny": 0.8}, 0.95),
		Config:  cfg(),
	})
	if out.Route != RouteEscalate {
		t.Errorf("an internally inconsistent answer was decided: %+v", out)
	}
}

func TestUnknownLevelOptionEscalates(t *testing.T) {
	out := Decide(Request{
		Answers: answers("probably-fine", map[string]float64{"probably-fine": 0.99}, 0.99),
		Config:  cfg(),
	})
	if out.Route != RouteEscalate {
		t.Errorf("an unknown verdict label was decided: %+v", out)
	}
}

func TestNoFactsMeansNoFactsEscalation(t *testing.T) {
	// A native tool (apply_patch, read) has no shell command to parse. Passing
	// nil facts must not be an escalation, or every non-shell call would skip
	// the cheap judge — but the level still has to be decisive.
	out := Decide(Request{
		Facts:   nil,
		Answers: answers("allow", map[string]float64{"allow": 0.97, "confirm": 0.03}, 0.95),
		Config:  cfg(),
	})
	if out.Route != RouteDecided || out.Level != judge.Allow {
		t.Errorf("a clean native call was not decided: %+v", out)
	}
}

func TestRationaleIsLocalizedAndFromTheLabel(t *testing.T) {
	// A decision model returns labels, not prose. The sentence the user reads
	// is assembled here, so it is identical for the same label every time.
	out := Decide(Request{
		Answers: answers("deny", map[string]float64{"deny": 0.95, "allow": 0.05}, 0.9,
			func(m map[string]jev.Answer) {
				m[QWhy] = choice("credential_access", map[string]float64{"credential_access": 0.9}, 0.9)
			}),
		Config: cfg(),
		Lang:   "zh",
	})
	if !strings.Contains(out.Rationale, "凭据") {
		t.Errorf("zh rationale = %q, want the credential wording", out.Rationale)
	}

	en := Decide(Request{
		Answers: answers("deny", map[string]float64{"deny": 0.95, "allow": 0.05}, 0.9,
			func(m map[string]jev.Answer) {
				m[QWhy] = choice("credential_access", map[string]float64{"credential_access": 0.9}, 0.9)
			}),
		Config: cfg(),
		Lang:   "en",
	})
	if !strings.Contains(en.Rationale, "credential") {
		t.Errorf("en rationale = %q, want the credential wording", en.Rationale)
	}
}

func TestUnknownReasonLabelDegradesToUnknown(t *testing.T) {
	out := Decide(Request{
		Answers: answers("allow", map[string]float64{"allow": 0.97, "confirm": 0.03}, 0.95,
			func(m map[string]jev.Answer) {
				m[QWhy] = choice("because-i-said-so", map[string]float64{"because-i-said-so": 1}, 1)
			}),
		Config: cfg(),
	})
	if out.Route != RouteDecided {
		t.Fatalf("escalated: %s", out.Escalation)
	}
	if !strings.Contains(out.Rationale, "visible") && !strings.Contains(out.Rationale, "无法判断") {
		t.Errorf("rationale = %q, want the unknown wording", out.Rationale)
	}
}

func TestRiskLabels(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{0.2, "low"},
		{1.0, "medium"},
		{1.9, "medium"},
		{2.5, "high"},
		{3.7, "critical"},
	}
	for _, c := range cases {
		out := Decide(Request{
			Answers: answers("allow", map[string]float64{"allow": 0.97, "confirm": 0.03}, 0.95,
				func(m map[string]jev.Answer) { m[QR] = score(c.score) }),
			Config: cfg(),
		})
		if out.Risk != c.want {
			t.Errorf("score %v -> risk %q, want %q", c.score, out.Risk, c.want)
		}
	}
}

func TestQuestionsUseTheThreePrimitives(t *testing.T) {
	qs := Questions()
	var choices, scores, nouls int
	for _, q := range qs {
		switch q.Type {
		case jev.Choice:
			choices++
		case jev.Score:
			scores++
		case jev.Noul:
			nouls++
		}
		if err := q.Validate(); err != nil {
			t.Errorf("question %+v is invalid: %v", q, err)
		}
	}
	// One verdict, one risk grade, and the independent signals.
	if choices != 2 {
		t.Errorf("choice questions = %d, want 2 (level and reason)", choices)
	}
	if scores != 1 {
		t.Errorf("score questions = %d, want 1", scores)
	}
	if nouls != 3 {
		t.Errorf("noul questions = %d, want 3", nouls)
	}
	// Every question id the policy reads must exist in the request, or the
	// policy would silently read a missing answer as a clean signal.
	for _, id := range []string{QL, QR, QRev, QSec, QInt, QWhy} {
		if _, ok := qs[id]; !ok {
			t.Errorf("the policy reads %q but the request never asks it", id)
		}
	}
}

func TestQuestionIDSetIsStable(t *testing.T) {
	got := strings.Join(sortedQuestionIDs(), ",")
	want := "irreversible,level,matches_intent,reason,risk,touches_secrets"
	if got != want {
		t.Errorf("question ids = %q, want %q", got, want)
	}
}

func TestDecideIsTotal(t *testing.T) {
	// Every combination must produce an outcome, and the only level reachable
	// without a clean bill is Deny or Sandbox — never Allow.
	levels := []string{"allow", "confirm", "contain", "deny", "", "bogus"}
	confs := []float64{0, 0.3, 0.7, 0.95}
	probs := []map[string]float64{
		{"allow": 0.99, "deny": 0.01},
		{"allow": 0.51, "confirm": 0.49},
		{"deny": 0.8, "allow": 0.2},
	}
	dangers := []float64{0.01, 0.5, 0.9}
	for _, level := range levels {
		for _, conf := range confs {
			for _, p := range probs {
				for _, danger := range dangers {
					as := answers(level, p, conf)
					as[QRev] = noul(danger)
					as[QSec] = noul(danger)
					out := Decide(Request{Answers: as, Config: cfg()})
					if out.Route == RouteEscalate {
						continue
					}
					// Decided: the level must be one of the four, and an
					// Allow only with a clean danger reading.
					switch out.Level {
					case judge.Deny, judge.Contain:
					case judge.Allow:
						if danger >= cfg().DangerConfirm {
							t.Errorf("allow decided despite danger %v (level=%q probs=%v)", danger, level, p)
						}
						if level != "allow" {
							t.Errorf("decided allow from level %q", level)
						}
					default:
						t.Errorf("unexpected level %v", out.Level)
					}
				}
			}
		}
	}
}
