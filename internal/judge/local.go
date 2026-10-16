package judge

// The local rule table: the routine grader for a run that has no decision model.
//
// # Why it exists
//
// The reviewer is a model call. With the decision model configured that is ~0.8s
// per gated call; without it the session's own model answers, measured at ~2.2s.
// Either way the user pays it on every command, including the ones no reasonable
// reviewer would pause over — `ls`, `grep`, `git status`, `go test`. The local
// table removes that cost for exactly those calls.
//
// # Where it sits
//
// It is neither an exemption nor a second opinion. The pipeline the gate runs is
//
//	static floor → the routine grader → session model
//
// assembled in cli/run.NewReviewer, and the routine grader is ONE of two things:
// the decision model when its credential resolves, this table otherwise. They
// answer the same question — is this call routine? — so a run uses one of them,
// with whatever neither can settle going to the session model. Chaining them
// would have routed the decision model's escalations here, and an escalation
// means the call deserves a model's judgment, not a cheaper grader's.
//
// There is no switch on it, because it is the fallback: a fallback that can be
// turned off is a fallback that leaves a run paying a model round trip for `ls`.
//
// # What it decides
//
// Four tiers out of the table, and the line is drawn so that a wrong answer is
// only ever possible in the direction of asking:
//
//	READONLY          → allow      it reads, lists or inspects
//	LOCAL_EFFECTS     → confirm    ordinary local work
//	EXTERNAL_EFFECTS  → confirm    ordinary outbound work
//	DANGEROUS         → defer      destructive or opaque: the model weighs intent
//	UNKNOWN           → defer      no rule: the model decides
//
// "Confirm" is the tier word the reviewer's own policy uses for
// state-changing-but-recoverable work, and it costs no model call: the gate
// settles a Confirm by the mode it is in — `auto` runs it, `ask` asks the user.
// That is what makes the table cheap in `auto` without giving it a policy of its
// own.
//
// Nothing here skips the gate: every call is graded by the gate's chain as
// before, and the table only answers some of them without a model. The shape it
// deliberately does NOT have is the one this project removed once already — an
// outermost allow that pre-empts the gate's own classification is an exemption
// wearing a classifier's name, and an exemption needs machinery (a kill switch, a
// credential re-check inside itself, escalation carve-outs) to justify itself.
// Deciding only the routine tiers is what makes that machinery unnecessary.
//
// Two mistakes are guarded explicitly, because each is a way a table gets a
// dangerous command wrong: a privilege escalator is never unwrapped (`sudo ls` is
// not `ls`), and a redirect that writes is a mutation whatever the program is
// (`ls > out` writes `out`).
//
// # The table
//
// riskRules (risk_table.go) is generated from bash-classify (MIT, Filip
// Prochazka, https://github.com/fprochazka/bash-classify), whose two-axis scheme
// — what a command does, and how much attention it deserves — is a better shape
// than one danger score; gen_risk_table.go reproduces it. Two hand-written
// supplements sit beside it: riskBuiltins, for the shell's own commands (a table
// of PATH programs does not cover them, and most shell lines are made of them),
// and riskExtras, for gaps in the reference.

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/shellfacts"
)

// LocalSource names this stage in a Verdict, so a card or a note can say which
// judge answered — the same courtesy the reviewer's own Source string pays.
const LocalSource = "local"

// riskBuiltins covers the shell's OWN commands, which the generated table does
// not: that table describes programs found on PATH, and most shell lines are
// builtins. Without this the commonest command in a coding session — `cd <dir>`
// — has no rule, and since a command with no rule is deferred, a line as
// harmless as `cd ~/work && grep -rn foo .` was handed to a model. Measured on a
// real session: 715 of 731 bash calls began with `cd`, and every one of them
// deferred for that reason alone.
//
// The classifications use the same vocabulary, and the two edges are where the
// care went: the commands that only inspect or only move the shell's own state
// read, the ones that RUN OTHER TEXT (`eval`, `exec`, `source`) are DANGEROUS
// because what they run is not in this command line, and the ones that change
// the shell's environment are local effects — they do not touch the filesystem,
// but neither are they reads, and calling them reads would make `export
// PATH=…` invisible in a review.
var riskBuiltins = map[string]Classification{
	// Inspection and the shell's own bookkeeping.
	"cd": ReadOnly, "pwd": ReadOnly, "echo": ReadOnly, "printf": ReadOnly,
	"test": ReadOnly, "[": ReadOnly, "true": ReadOnly, "false": ReadOnly,
	":": ReadOnly, "read": ReadOnly, "type": ReadOnly, "shift": ReadOnly,
	"history": ReadOnly, "times": ReadOnly, "trap": ReadOnly,

	// The shell's environment: real state, confined to this process.
	"export": LocalEffects, "unset": LocalEffects, "set": LocalEffects,
	"umask": LocalEffects, "alias": LocalEffects, "unalias": LocalEffects,
	"local": LocalEffects, "declare": LocalEffects, "typeset": LocalEffects,

	// Runs text this command line does not contain: nothing here can be read.
	"eval": Dangerous, "exec": Dangerous, "source": Dangerous, ".": Dangerous,
	"return": Dangerous, "break": Dangerous, "continue": Dangerous,
	"wait": Dangerous, "jobs": Dangerous, "fg": Dangerous, "bg": Dangerous,
	"kill": Dangerous, "suspend": Dangerous, "disown": Dangerous,
}

// riskExtras fills obvious gaps in the generated table, for tools this project's
// own workflow cannot run a session without. Measured on a real session: `gofmt`
// alone accounted for 112 of 214 deferrals, and it deferred only because the
// reference table (which has black, isort, eslint, mypy, tsc, javac) has no Go
// tooling beyond `go` itself.
//
// This is data, not a heuristic: each entry is a classification someone decided,
// in the open, and the same commandRule shape means an entry can carry its own
// flags. `rg` is the reason that matters — it is read-only EXCEPT for `--pre`,
// which runs a program per file, so the flag is stated rather than left to a
// reader's memory of ripgrep's manual.
//
// Keep the list short and each addition defensible: the reference is the source
// of record for everything else, and a table that grows by request slowly stops
// being reviewable.
var riskExtras = map[string]commandRule{
	"rg": {
		class: ReadOnly,
		flags: map[string]Classification{"--pre": Dangerous},
	},
	"gofmt": {
		class: ReadOnly,                                      // it prints a reformatted file...
		flags: map[string]Classification{"-w": LocalEffects}, // ...unless asked to write
	},
}

// Classification is what a command does to the world. Only ReadOnly is decided
// locally; the rest name the tiers a future classifier could use, and are the
// vocabulary of bash-classify's scheme.
type Classification string

const (
	// ReadOnly touches no state: it reads, lists, or inspects.
	ReadOnly Classification = "READONLY"
	// LocalEffects changes files or local state only.
	LocalEffects Classification = "LOCAL_EFFECTS"
	// ExternalEffects reaches another system: the network, a cluster, a service.
	ExternalEffects Classification = "EXTERNAL_EFFECTS"
	// Dangerous is destructive or hard to reverse.
	Dangerous Classification = "DANGEROUS"
	// Unknown is a program or subcommand this machine has no rule for. It is
	// ranked above ExternalEffects and below Dangerous, and — like Dangerous —
	// it is never decided locally.
	Unknown Classification = "UNKNOWN"
)

// Risk is how much attention a command deserves, independently of what it does:
// `git status` and `rm -rf` differ in both axes, but so do `ls` and `cat
// ~/.ssh/id_rsa`, which are both read-only.
// Local grades a shell command from the rule table, and defers the tiers it
// cannot weigh to Next.
//
// It stands where the decision model stands — the classifier the approval gate
// consults on every gated call — so that a run WITHOUT a decision-model
// credential is not left with a ~2.2s session-model round trip per command. JEV
// goes in front of it when it is configured (see cli/run.NewReviewer), so the
// two never compete: whichever is present answers, and what it cannot answer
// falls to the session model.
type Local struct {
	// Next grades everything this stage defers. It is the reviewer that would
	// have graded the call anyway, so deferring is not a fallback — it is the
	// pre-existing path.
	Next Classifier
}

// Classify implements Classifier: grade the command, decide the routine tiers,
// defer the rest.
func (l Local) Classify(ctx context.Context, tool string, args json.RawMessage) Verdict {
	if v, ok := localGradeOf(tool, args, ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))); ok {
		if NetworkRequested(tool, args) {
			// The local table may approve network only for calls it has actually
			// classified as outbound work. A read-only-looking or local-effects
			// command asking for network needs the session reviewer's judgment;
			// an Allow/Confirm for some other effect is NOT network permission.
			if v.Level != Confirm || v.Risk != "medium" || len(v.Reasons) == 0 ||
				v.Reasons[0] != localRationale(v.Lang, "external_effects") {
				return l.deferToNext(ctx, tool, args)
			}
			v.Network = true
		}
		if escalationRequested(args) {
			// The source table classifies the command, not why it needs files
			// beyond the sandbox. Only a reviewer with conversation context can
			// explicitly authorize a one-call escape.
			return l.deferToNext(ctx, tool, args)
		}
		return v
	}
	return l.deferToNext(ctx, tool, args)
}

func (l Local) deferToNext(ctx context.Context, tool string, args json.RawMessage) Verdict {
	if l.Next == nil {
		// No reviewer to defer to: fail closed with the same shape the LLM
		// classifier uses when it is not wired, so a missing reviewer is never an
		// allow.
		return Verdict{
			Level:   Confirm,
			Failed:  true,
			Source:  LocalSource,
			Reasons: []string{"no reviewer available, failing closed"},
		}
	}
	return l.Next.Classify(ctx, tool, args)
}

// localGradeOf decides the call, or reports that it cannot, with the rationale
// written in the language the conversation is held in — the same rule JEV's
// layer follows: a decision model returns labels, and the LAYER owns the wording.
//
// The rule it applies is the one thing a table can be trusted for: naming what a
// command IS. Whether a particular destructive or unmodelled invocation should
// be allowed depends on what the user asked for, which is the model's business,
// so those tiers are deferred rather than guessed:
//
//	READONLY          → allow      it reads, lists or inspects
//	LOCAL_EFFECTS     → confirm    ordinary local work (installs, edits, builds)
//	EXTERNAL_EFFECTS  → confirm    ordinary outbound work (fetches, cluster reads)
//	DANGEROUS         → defer      destructive or opaque; the model weighs intent
//	UNKNOWN           → defer      no rule: the model decides
//
// "Confirm" is the same tier word the reviewer's own policy uses for
// state-changing-but-recoverable work, and it does not mean a model call: the
// gate settles a Confirm by the mode it is in — auto runs it, ask asks the user.
// That is what makes this stage cheap in `auto` without turning it into a second
// policy of its own.
func localGradeOf(_ string, args json.RawMessage, lang string) (Verdict, bool) {
	cmd, ok := bashCommand(args)
	if !ok || strings.TrimSpace(cmd) == "" {
		return Verdict{}, false
	}
	plan := shellfacts.Parse(cmd)
	if !plan.Parsed {
		// The shell cannot read this either; nothing here will run, and a
		// half-read command line is the last thing to grade from a table.
		return Verdict{}, false
	}

	worst := ReadOnly
	reason := ""
	for _, c := range plan.AllCommands() {
		if len(c.Argv) == 0 {
			// A bare redirect (`> file`): it runs no program, and the write is
			// caught below. Nothing to look up.
			continue
		}
		class, rule, known := classifyCommand(c)
		if !known && class == Unknown {
			// An anonymous program: no rule at all. Defer.
			return Verdict{}, false
		}
		if severity(class) > severity(worst) {
			worst, reason = class, rule
		}
	}
	// A redirect that writes a file is a file mutation whatever the program is:
	// `ls > out.txt` writes, and no rule about `ls` says so.
	for _, c := range plan.AllCommands() {
		for _, r := range c.Redirs {
			if r.Dup || r.Heredoc {
				continue
			}
			if !strings.HasPrefix(r.Op, "<") && !isDevNull(r.Target.Text) {
				if severity(LocalEffects) > severity(worst) {
					worst, reason = LocalEffects, "redirect"
				}
			}
		}
	}

	switch worst {
	case ReadOnly:
		return Verdict{
			Level:      Allow,
			Source:     LocalSource,
			Confidence: 1,
			Risk:       riskOf(ReadOnly),
			Lang:       lang,
			Reasons:    []string{localRationale(lang, "read_only")},
		}, true
	case LocalEffects, ExternalEffects:
		key := "local_effects"
		if worst == ExternalEffects {
			key = "external_effects"
		}
		if reason == "redirect" {
			key = "redirect_write"
		}
		return Verdict{
			Level:      Confirm,
			Source:     LocalSource,
			Confidence: 1,
			Risk:       riskOf(worst),
			Lang:       lang,
			Reasons:    []string{localRationale(lang, key)},
		}, true
	}
	return Verdict{}, false
}

// localRationale is the one line the user reads when the table answers. It is
// assembled here rather than by the table for the reason JEV's layer states on
// its own side: the table names what a command IS (a classification, a
// subcommand), and prose is the layer's job. Keeping it a small local table also
// means the wording moves with the vocabulary instead of with 168 rule files.
func localRationale(lang, key string) string {
	zh := map[string]string{
		"read_only":        "只读命令：只读取、列出或检查，不改变任何东西",
		"local_effects":    "常规本地操作：会改动本地文件或 shell 状态",
		"redirect_write":   "会写入文件：重定向的目标由 shell 打开",
		"external_effects": "常规外部操作：会访问网络或远端服务",
	}
	en := map[string]string{
		"read_only":        "reads only: it inspects, lists or fetches, and changes nothing",
		"local_effects":    "ordinary local work: it changes local files or shell state",
		"redirect_write":   "writes a file: the shell opens the redirect's target",
		"external_effects": "ordinary outbound work: it reaches the network or a remote service",
	}
	table := en
	if strings.EqualFold(strings.TrimSpace(lang), "zh") {
		table = zh
	}
	if text, ok := table[key]; ok {
		return text
	}
	return table["local_effects"]
}

// riskOf is the table's second axis, the one share of it this stage can state:
// how much attention the tier deserves. It travels on the verdict so the note
// the gate prints reads as a rating rather than as "risk: unknown".
func riskOf(c Classification) string {
	switch c {
	case ReadOnly:
		return "low"
	case LocalEffects, ExternalEffects:
		return "medium"
	default:
		return "high"
	}
}

// classifyCommand reads one parsed command against the table: the program's own
// rule, narrowed by its subcommand when the table knows that subcommand, and
// raised by any option that makes the invocation a worse act than its base
// (`git push` reaches another system; `git push --force` destroys remote work).
//
// known=false means the program has no rule at all, which is the one case worth
// distinguishing from a rule that says Unknown: a program nobody has described
// is not the same as one described as unmodellable, and only the caller knows
// whether it wants to guess.
func classifyCommand(c shellfacts.Command) (Classification, string, bool) {
	i, ok := unwrapProgram(c.Argv)
	if !ok {
		return Unknown, "", false
	}
	prog := path.Base(c.Argv[i].Text)
	rule, known := riskRules[prog]
	if !known {
		// The generated table's gaps that this project fills by hand (rg, gofmt —
		// see riskExtras).
		rule, known = riskExtras[prog]
	}
	if !known {
		// The shell's own commands are not in that table (see riskBuiltins), and
		// a shell line is mostly made of them.
		if class, ok := riskBuiltins[prog]; ok {
			return class, "the shell builtin " + prog + " is classified " + string(class), true
		}
		return Unknown, "", false
	}
	// A privilege escalator changes the act without changing the program: `ls` as
	// root is not `ls`, and the table's own rule for `sudo` says DANGEROUS. The
	// command word is unwrapped for the PATH reader (a boundary question wants the
	// paths the wrapped command names), so the check has to happen here rather
	// than in that unwrapping.
	for _, w := range c.Argv[:i] {
		switch path.Base(w.Text) {
		case "sudo", "doas":
			return Dangerous, "privilege escalation (" + path.Base(w.Text) + ")", true
		}
	}
	args := c.Argv[i+1:]

	class := rule.class
	what := "the table classifies " + prog + " as " + string(class)
	// Walk the subcommand tree rather than reading one word: the source nests
	// (`gh run list` is a subcommand of a subcommand), and stopping at the first
	// level reads the parent's answer — for gh that is UNKNOWN, which would defer
	// every `gh run list` to a model.
	var levels []commandRule
	for rest := args; ; {
		levels = append(levels, rule)
		sub := subcommandName(rest)
		if sub == "" {
			break
		}
		subRule, ok := rule.subs[sub]
		if !ok {
			break
		}
		rule, class = subRule, subRule.class
		what = prog + " " + sub + " is classified " + string(class)
		rest = rest[afterWord(rest, sub):]
	}
	if raised, flag, ok := raisingFlag(levels, args); ok {
		class = raised
		what = prog + " with " + flag + " is classified " + string(class)
	}
	return class, what, true
}

// afterWord returns the index just past the first word whose text is want, so a
// walk down the subcommand tree can consume the words it matched.
func afterWord(words []shellfacts.Word, want string) int {
	for i, w := range words {
		if w.Text == want {
			return i + 1
		}
	}
	return len(words)
}

// raisingFlag reports the worst option in this argv, when the table says an
// option makes the invocation a worse act than the command or subcommand alone.
// Every level of the tree is consulted, because an option can be declared at any
// of them (`git push -f` states the force at the subcommand, `curl -T` at the
// command).
//
// A short flag is matched both whole and letter by letter (`-f`, and the `f` in
// `-fu`): the source writes its overrides per letter, and reading a cluster as
// one unknown token would miss the force in `git push -fu`. Matching a letter
// that the source did not intend over-rates the call, which lands it with the
// model rather than in an allow — the safe direction for an approximation.
func raisingFlag(levels []commandRule, args []shellfacts.Word) (Classification, string, bool) {
	var out Classification
	var flagName string
	for _, a := range args {
		text := a.Text
		if !strings.HasPrefix(text, "-") || text == "-" {
			continue
		}
		candidates := []string{text}
		if eq := strings.IndexByte(text, '='); eq > 0 {
			candidates = append(candidates, text[:eq])
		}
		if !strings.HasPrefix(text, "--") {
			for _, r := range strings.TrimPrefix(text, "-") {
				candidates = append(candidates, "-"+string(r))
			}
		}
		for _, cand := range candidates {
			for _, rule := range levels {
				raised, ok := rule.flags[cand]
				if !ok {
					continue
				}
				if severity(raised) > severity(out) {
					out, flagName = raised, cand
				}
			}
		}
	}
	return out, flagName, flagName != ""
}

// subcommandName returns the word a program would read as its subcommand: the
// first argument that is not a flag.
//
// It does not step over a flag's VALUE, so `git -C /repo status` reads `/repo`
// as the subcommand and falls back to the program's own rule. That fallback is
// the conservative direction (a program's own class is never milder than the
// subcommand's in this table — `git` is DANGEROUS, `git status` is READONLY), so
// the cost of the approximation is a model call, never an unreviewed command.
func subcommandName(args []shellfacts.Word) string {
	for _, a := range args {
		if a.Text == "" || strings.HasPrefix(a.Text, "-") {
			continue
		}
		return a.Text
	}
	return ""
}

// isDevNull reports whether a redirect target is the bit bucket, which writes
// nothing worth containing: `2>/dev/null` is a stream discard, not a file.
func isDevNull(target string) bool {
	return target == "/dev/null"
}

// severity orders the classifications, so an expression is as severe as its
// worst command. DANGEROUS outranks UNKNOWN (a known-destructive command is a
// stronger statement than an unmodelled one), which is the order bash-classify
// uses.
func severity(c Classification) int {
	switch c {
	case ReadOnly:
		return 0
	case LocalEffects:
		return 1
	case ExternalEffects:
		return 2
	case Unknown:
		return 3
	case Dangerous:
		return 4
	}
	return 3
}
