// Package shellfacts describes what a shell command will do, parsed rather than
// guessed at.
//
// The rule that shapes every line: this package produces FACTS, never verdicts.
// It says "this argv vector, this redirect, this nested command, this word is
// dynamic" — and nothing about whether any of it is safe. Deciding belongs to
// the layers above, because a permission decision needs context a parser does
// not have (what the user asked for, which directory is trusted, what the
// program does with its arguments).
//
// Why parse at all. A guard that pattern-matches the raw command string is
// answering a different question than the shell will: bash performs quote
// removal, parameter expansion, command substitution and field splitting AFTER
// any string match has run, so the bytes a matcher sees are not the argv that
// executes. That gap is where shell-injection bypasses live (`r”m` becomes
// `rm`; `$IFS` becomes whitespace; `$(echo rm)` becomes a program name). This
// package closes the gap by handing the caller the argv vectors bash would
// build, instead of making it infer them from text.
//
// What it deliberately does NOT do:
//
//   - It never expands anything. No variable is looked up, no command is run,
//     no glob touches the filesystem. A word carrying `$VAR` is reported as
//     dynamic WITH the variable's name, and a word carrying `$(...)` gets its
//     body parsed as a nested plan — because "this will execute something" is
//     the fact that matters, and executing it to learn its output would be the
//     exact mistake this package exists to avoid.
//   - It never decides. There is no "is this safe", no allowlist, no
//     confidence. A caller that wants a verdict builds one from these facts.
//
// A parse failure is itself a fact: a real command line that this parser cannot
// read (an unterminated quote, a zsh-only construct) leaves the shell reading
// more input, so callers must treat Parsed == false as "unknown", never as
// "harmless".
package shellfacts

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Op is an operation on a path. It mirrors what a shell or program does to the
// path, not what any policy thinks about it.
type Op uint8

const (
	// Read opens or lists the path.
	Read Op = 1 << iota
	// Write creates, truncates, appends to or deletes the path.
	Write
	// Execute runs the path as a program (a path in command position).
	Execute
	// UnknownOp means the path was named but the operation depends on program
	// semantics this package does not have. A caller must treat it as possibly
	// destructive rather than guessing.
	UnknownOp
)

func (o Op) String() string {
	switch {
	case o == Read:
		return "read"
	case o == Write:
		return "write"
	case o == Execute:
		return "execute"
	case o == UnknownOp:
		return "unknown"
	}
	return "mixed"
}

// PartKind is what one segment of a word is. The kinds exist so a caller can
// see WHY a word is dynamic without re-parsing it.
type PartKind string

const (
	// LiteralPart is ordinary text.
	LiteralPart PartKind = "literal"
	// QuotedPart is single- or double-quoted text. Quotes are removed from the
	// text, because that is what the shell does before the word reaches argv.
	QuotedPart PartKind = "quoted"
	// VariablePart is `$VAR` or `${VAR}`. The value is NOT resolved.
	VariablePart PartKind = "variable"
	// CommandSubstPart is `$(...)` or backticks: a command that will run before
	// this one. Its body is parsed into Nested.
	CommandSubstPart PartKind = "command-substitution"
	// ArithmPart is `$((...))`.
	ArithmPart PartKind = "arithmetic"
	// GlobPart is an unquoted `*`, `?` or `[...]`: a path the shell computes by
	// reading a directory.
	GlobPart PartKind = "glob"
	// ProcSubstPart is `<(cmd)` or `>(cmd)`: a FIFO the shell creates.
	ProcSubstPart PartKind = "process-substitution"
	// ExtGlobPart is bash's `@(a|b)`, `!(a)` and friends: a word that decides
	// itself from the filesystem.
	ExtGlobPart PartKind = "extglob"
	// TildePart is a leading unquoted `~`: the shell replaces it with a home
	// directory, so the word's value is not the text.
	TildePart PartKind = "tilde"
)

// Part is one segment of a word.
type Part struct {
	Kind PartKind
	// Text is the literal text (for LiteralPart and QuotedPart) or the variable
	// name (for VariablePart). Empty otherwise.
	Text string
	// Nested is the parsed body of a command substitution. It is present so a
	// caller can see what will run; it is never executed here.
	Nested *Plan
}

// Word is one argument as the shell would build it, as far as that can be known
// without expanding anything.
type Word struct {
	// Text is the word's text with quotes removed and literals joined. It is
	// the argv spelling when Dynamic is false, and a best-effort spelling
	// otherwise — a word spelled `"$HOME"/x` reads as `/x` with a variable part,
	// so callers must consult Dynamic before trusting Text as a path.
	Text string
	// Dynamic reports that some part expands at runtime, so this word's argv
	// value is not fixed by the text.
	Dynamic bool
	// Parts is every segment, in order.
	Parts []Part
}

// Redirect is one redirection, with its operation already decided because shell
// redirection semantics are fixed (`>` writes, `<` reads) — unlike a path passed
// as an argument, whose operation depends on the program.
type Redirect struct {
	// Fd is the file descriptor, 1 for stdout when unspecified.
	Fd int
	// Target is where the descriptor points. For a descriptor duplication
	// (`2>&1`) it is the other descriptor as text, and Dup is true.
	Target Word
	// Op is the shell's operator text: ">", ">>", "<", "<>", ">|", "<<", "<&",
	// ">&", "&>", "&>>".
	Op string
	// Dup reports a descriptor duplication, which names no path.
	Dup bool
	// Heredoc reports `<<`/`<<-`: the body is further shell input.
	Heredoc bool
}

// Command is one simple command in the program, with the shape the caller needs
// in order to reason about it.
type Command struct {
	// Index is this command's position in the program's execution order.
	Index int
	// Argv is the argument vector, command position first.
	Argv []Word
	// Assigns are the leading `VAR=value` words. They are separated from Argv
	// because a shell treats them as environment for this command, not as
	// arguments — and because an assignment in command position is how
	// `LD_PRELOAD=… ls` runs something other than what it appears to.
	Assigns []Word
	// Redirs are this command's redirections.
	Redirs []Redirect
	// Nested holds the bodies of any command or process substitutions inside
	// this command's words, in order of appearance.
	Nested []*Plan
	// Background reports a trailing `&`.
	Background bool
	// Negated reports a leading `!`.
	Negated bool
	// InPipeline reports that this command's output feeds another.
	InPipeline bool
	// InSubshell reports that this command runs in a subshell rather than the
	// shell's own process.
	InSubshell bool
}

// Program returns the command word's text and whether it was a plain literal.
// A dynamic command word means the program itself is computed at runtime, which
// is the most important fact about a command and the one a string matcher
// cannot see: `$(echo rm) -rf /` has no literal program at all.
func (c Command) Program() (string, bool) {
	if len(c.Argv) == 0 {
		return "", false
	}
	if c.Argv[0].Dynamic {
		return c.Argv[0].Text, false
	}
	return c.Argv[0].Text, true
}

// Args returns the words after the command word. They are NOT the same as argv
// with the program removed when an assignment prefix is present, which is why
// Assigns is separate.
func (c Command) Args() []Word {
	if len(c.Argv) == 0 {
		return nil
	}
	return c.Argv[1:]
}

// Plan is what one command line will do.
type Plan struct {
	// Raw is the text this plan was parsed from, unchanged. It is kept because
	// the parser can be wrong: a caller comparing Raw against Commands can see
	// a construct this layer represented poorly, and the model reviewer (which
	// reads prose) can still see the original.
	Raw string
	// Parsed reports whether the text parsed as a complete program. False means
	// the shell would keep reading (an unterminated quote) or the construct is
	// not POSIX/bash — either way nothing here may be treated as complete.
	Parsed bool
	// ParseError is the parser's message when Parsed is false.
	ParseError string
	// Commands are the TOP-LEVEL simple commands, in execution order. A command
	// substitution's own commands are NOT here: they hang off the containing
	// command's Nested field, so the structure of the line stays readable.
	//
	// That split is a trap for a caller enumerating commands to decide
	// something: `echo "$(rm -rf /x)"` has one top-level command (`echo`), and
	// iterating this slice alone would never see the `rm`. Use AllCommands for
	// any enumeration that must be complete — it is the safe default for
	// deciding, and this field is for showing.
	Commands []Command
	// Constructs names command forms this layer walked past without describing:
	// a loop, a function definition, a conditional. They are reported rather
	// than ignored, because "there is a for loop here" is exactly the kind of
	// fact a caller needs in order to decline to reason about the command.
	Constructs []string
}

// AllCommands returns every command in the plan, including the ones inside
// command and process substitutions, in execution order: a substitution runs
// before the command containing it, so its commands come first.
//
// This is the enumeration a caller deciding something must use. Commands alone
// is a trap there: `echo "$(rm -rf /x)"` has exactly one top-level command, and
// the `rm` is reachable only through Nested. Two ways to enumerate a plan is
// one too many, so the safe one is named for what it is and the other is
// documented as the display view.
func (p *Plan) AllCommands() []Command {
	if p == nil {
		return nil
	}
	return p.allCommands()
}

func (p *Plan) allCommands() []Command {
	var out []Command
	for _, c := range p.Commands {
		for _, n := range c.Nested {
			if n == nil {
				continue
			}
			out = append(out, n.allCommands()...)
		}
		out = append(out, c)
	}
	return out
}

// Parse reads one command line. It never returns an error: a failure to parse
// is reported in the Plan, because callers must treat "could not read it" as a
// fact distinct from "there is nothing there".
func Parse(command string) *Plan {
	p := &Plan{Raw: command}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "command")
	if err != nil {
		p.ParseError = err.Error()
		return p
	}
	p.Parsed = true
	b := &builder{}
	b.walkStmts(file.Stmts, false, false)
	p.Commands = b.commands
	p.Constructs = b.constructs
	return p
}

// builder accumulates commands across the whole tree.
type builder struct {
	commands   []Command
	constructs []string
}

func (b *builder) add(c Command) {
	c.Index = len(b.commands)
	b.commands = append(b.commands, c)
}

// walkStmts visits statements in source order.
func (b *builder) walkStmts(stmts []*syntax.Stmt, inPipeline, inSubshell bool) {
	for _, st := range stmts {
		b.walkStmt(st, inPipeline, inSubshell)
	}
}

func (b *builder) walkStmt(st *syntax.Stmt, inPipeline, inSubshell bool) {
	if st == nil || st.Cmd == nil {
		return
	}
	switch cmd := st.Cmd.(type) {
	case *syntax.CallExpr:
		b.add(buildCommand(cmd, st, inPipeline, inSubshell))

	case *syntax.BinaryCmd:
		// `a && b`, `a || b`, `a | b`. The left side is a fact-fetch that
		// matters (a pipeline's stages run concurrently, and `&&` means the
		// right side may not run at all), so it is recorded as structure.
		pipe := cmd.Op == syntax.Pipe || cmd.Op == syntax.PipeAll
		b.walkStmt(cmd.X, pipe, inSubshell)
		b.walkStmt(cmd.Y, inPipeline, inSubshell)

	case *syntax.Subshell:
		b.constructs = append(b.constructs, "subshell")
		b.walkStmts(cmd.Stmts, inPipeline, true)

	case *syntax.Block:
		b.constructs = append(b.constructs, "block")
		b.walkStmts(cmd.Stmts, inPipeline, inSubshell)

	case *syntax.IfClause:
		b.walkIf(cmd, inPipeline, inSubshell)

	case *syntax.ForClause:
		b.constructs = append(b.constructs, "for")
		b.walkStmts(cmd.Do, inPipeline, inSubshell)

	case *syntax.WhileClause:
		b.constructs = append(b.constructs, "while")
		b.walkStmts(cmd.Cond, inPipeline, inSubshell)
		b.walkStmts(cmd.Do, inPipeline, inSubshell)

	case *syntax.CaseClause:
		b.constructs = append(b.constructs, "case")
		for _, item := range cmd.Items {
			b.walkStmts(item.Stmts, inPipeline, inSubshell)
		}

	case *syntax.FuncDecl:
		// A function definition runs nothing by itself, but every later call to
		// the name runs its body — so the body is walked and the definition is
		// named. A caller seeing "function" knows the earlier shapes it read are
		// not the whole story.
		b.constructs = append(b.constructs, "function "+cmd.Name.Value)
		b.walkStmt(cmd.Body, inPipeline, inSubshell)

	case *syntax.ArithmCmd:
		b.constructs = append(b.constructs, "arithmetic command")

	case *syntax.TestClause:
		b.constructs = append(b.constructs, "test")

	case *syntax.DeclClause:
		b.constructs = append(b.constructs, "declaration "+cmd.Variant.Value)

	case *syntax.CoprocClause:
		b.constructs = append(b.constructs, "coprocess")

	case *syntax.LetClause:
		b.constructs = append(b.constructs, "let")

	case *syntax.TimeClause:
		b.constructs = append(b.constructs, "time")
		b.walkStmt(cmd.Stmt, inPipeline, inSubshell)

	default:
		b.constructs = append(b.constructs, "unsupported command form")
	}
}

// walkIf records one `if`/`elif`/`else` chain. The chain is a linked list of
// IfClause nodes rather than nested statements — Else is itself an IfClause —
// so the walk recurses on the clause, not on a Stmt. Every branch is visited
// even though only one will run: a caller reasoning about a command must see
// all of them, because which branch executes is a runtime fact it cannot know.
func (b *builder) walkIf(clause *syntax.IfClause, inPipeline, inSubshell bool) {
	for clause != nil {
		b.constructs = append(b.constructs, "if")
		b.walkStmts(clause.Cond, inPipeline, inSubshell)
		b.walkStmts(clause.Then, inPipeline, inSubshell)
		clause = clause.Else
	}
}

// buildCommand records one simple command.
func buildCommand(call *syntax.CallExpr, st *syntax.Stmt, inPipeline, inSubshell bool) Command {
	c := Command{
		Background: st.Background,
		Negated:    st.Negated,
		InPipeline: inPipeline,
		InSubshell: inSubshell,
	}
	for _, a := range call.Assigns {
		if a == nil || a.Name == nil {
			continue
		}
		c.Assigns = append(c.Assigns, Word{Text: a.Name.Value, Parts: []Part{{Kind: LiteralPart, Text: a.Name.Value}}})
	}
	for _, arg := range call.Args {
		w := buildWord(arg)
		c.Argv = append(c.Argv, w)
		c.Nested = append(c.Nested, nestedPlans(arg)...)
	}
	for _, r := range st.Redirs {
		c.Redirs = append(c.Redirs, buildRedirect(r))
		c.Nested = append(c.Nested, nestedPlans(r.Word)...)
	}
	return c
}

func buildRedirect(r *syntax.Redirect) Redirect {
	out := Redirect{Op: r.Op.String()}
	if r.N != nil {
		if v, err := parseFd(r.N.Value); err == nil {
			out.Fd = v
		}
	} else {
		out.Fd = defaultFd(r.Op)
	}
	switch r.Op {
	case syntax.DplIn, syntax.DplOut:
		out.Dup = true
	case syntax.Hdoc, syntax.DashHdoc:
		out.Heredoc = true
	}
	if r.Word != nil {
		out.Target = buildWord(r.Word)
	}
	return out
}

// defaultFd is the descriptor a redirect applies to when none is written.
func defaultFd(op syntax.RedirOperator) int {
	switch op {
	case syntax.RdrIn, syntax.RdrInOut, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return 0
	default:
		return 1
	}
}

func parseFd(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errBadFd
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

var errBadFd = errString("not a file descriptor")

type errString string

func (e errString) Error() string { return string(e) }

// buildWord converts one word into its parts, flattening quotes (the shell
// removes them before argv) while remembering that a quote was there.
func buildWord(w *syntax.Word) Word {
	out := Word{}
	if w == nil {
		return out
	}
	for _, part := range w.Parts {
		appendPart(&out, part, false)
	}
	return out
}

// appendPart records one word part, recursing into quotes. inQuotes reports
// whether this part sits inside a double-quoted string, where `*` is literal
// text rather than a glob and a backslash escapes only a few characters.
//
// Quote removal happens here, and quote removal only. The shell removes quotes
// and resolves backslash escapes BEFORE argv exists, and both are lexical: they
// depend on nothing but the text. Expansion is the opposite — it needs the
// runtime environment — so it is reported (a VariablePart naming the variable)
// rather than performed.
//
// That distinction is why this package does not call mvdan's expand.Literal,
// which looks like the obvious tool for the job. Measured, with a nil
// environment it returns "rm-rf/" for `rm$IFS-rf$IFS/` and "/x" for
// `"$HOME"/x`: it silently drops the variable, so the text it hands back is a
// benign-looking lie about what will run. Refusing to compute the value is the
// safe direction; inventing one is not.
func appendPart(out *Word, part syntax.WordPart, inQuotes bool) {
	switch p := part.(type) {
	case *syntax.Lit:
		kind := LiteralPart
		if inQuotes {
			kind = QuotedPart
		}
		text, glob := unescape(p.Value, inQuotes)
		// A leading unquoted tilde is the shell's own rewrite, not literal data,
		// so it is reported as its own part.
		if !inQuotes && out.Text == "" && isTildePrefix(text) {
			out.Dynamic = true
			out.Parts = append(out.Parts, Part{Kind: TildePart, Text: text})
			out.Text += text
			return
		}
		out.Text += text
		out.Parts = append(out.Parts, Part{Kind: kind, Text: text})
		if glob {
			// An unquoted glob: the shell reads a directory to decide the word.
			out.Dynamic = true
			out.Parts = append(out.Parts, Part{Kind: GlobPart})
		}

	case *syntax.SglQuoted:
		out.Text += p.Value
		out.Parts = append(out.Parts, Part{Kind: QuotedPart, Text: p.Value})

	case *syntax.DblQuoted:
		for _, inner := range p.Parts {
			appendPart(out, inner, true)
		}

	case *syntax.ParamExp:
		out.Dynamic = true
		out.Parts = append(out.Parts, Part{Kind: VariablePart, Text: paramName(p)})

	case *syntax.CmdSubst:
		out.Dynamic = true
		out.Parts = append(out.Parts, Part{Kind: CommandSubstPart, Nested: parseCmdSubst(p)})

	case *syntax.ArithmExp:
		out.Dynamic = true
		out.Parts = append(out.Parts, Part{Kind: ArithmPart})

	case *syntax.ProcSubst:
		out.Dynamic = true
		out.Parts = append(out.Parts, Part{Kind: ProcSubstPart, Nested: parseProcSubst(p)})

	case *syntax.ExtGlob:
		out.Dynamic = true
		out.Parts = append(out.Parts, Part{Kind: ExtGlobPart})
	}
}

// paramName is the variable a parameter expansion names, for the common forms.
// A braced expansion with an operator (`${x:-y}`) still names x: what the
// operator does with it is a fact about the expansion, not about which variable
// is read.
func paramName(p *syntax.ParamExp) string {
	if p.Param == nil {
		return ""
	}
	return p.Param.Value
}

// unescape applies the shell's quote removal to one literal run and reports
// whether an unescaped glob metacharacter survived.
//
// Unquoted, a backslash escapes whatever follows: the backslash goes and the
// character stays as literal data. Inside double quotes it escapes only `$`,
// backtick, `"`, `\` and a newline; before anything else it is itself literal
// data and stays. A backslash-newline is a line continuation in both contexts.
//
// The glob flag exists because escaping decides whether a metacharacter IS one:
// `\*` is a literal asterisk, `*` is a pattern. Getting that backwards would
// report fixed text as dynamic, or a pattern as fixed.
func unescape(s string, inDoubleQuotes bool) (string, bool) {
	if !strings.Contains(s, "\\") {
		return s, !inDoubleQuotes && strings.ContainsAny(s, "*?[")
	}
	var b strings.Builder
	b.Grow(len(s))
	glob := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			next := s[i+1]
			if next == '\n' {
				i++ // line continuation: both characters go
				continue
			}
			if inDoubleQuotes {
				switch next {
				case '$', '`', '"', '\\':
					b.WriteByte(next)
					i++
					continue
				}
				// Not an escape in this context: the backslash is data.
				b.WriteByte(c)
				continue
			}
			// Unquoted: the escaped character is literal, so it is not a glob
			// metacharacter.
			b.WriteByte(next)
			i++
			continue
		}
		if !inDoubleQuotes && (c == '*' || c == '?' || c == '[') {
			glob = true
		}
		b.WriteByte(c)
	}
	return b.String(), glob
}

// isTildePrefix reports whether a literal run begins with the shell's tilde
// form: a bare `~`, `~/…`, or `~user`. All three expand, and only the first two
// expand to a value this process could know — which is why the fact is reported
// (Dynamic) rather than resolved.
func isTildePrefix(text string) bool {
	return strings.HasPrefix(text, "~")
}

func parseCmdSubst(cs *syntax.CmdSubst) *Plan {
	if cs == nil {
		return nil
	}
	nested := &Plan{Raw: "command substitution", Parsed: true}
	b := &builder{}
	b.walkStmts(cs.Stmts, false, true)
	nested.Commands = b.commands
	nested.Constructs = b.constructs
	return nested
}

func parseProcSubst(ps *syntax.ProcSubst) *Plan {
	if ps == nil {
		return nil
	}
	nested := &Plan{Raw: "process substitution", Parsed: true}
	b := &builder{}
	b.walkStmts(ps.Stmts, false, true)
	nested.Commands = b.commands
	nested.Constructs = b.constructs
	return nested
}

// nestedPlans returns the nested plans inside one word's substitutions.
func nestedPlans(w *syntax.Word) []*Plan {
	if w == nil {
		return nil
	}
	var out []*Plan
	var visit func(parts []syntax.WordPart)
	visit = func(parts []syntax.WordPart) {
		for _, part := range parts {
			switch p := part.(type) {
			case *syntax.DblQuoted:
				visit(p.Parts)
			case *syntax.CmdSubst:
				if n := parseCmdSubst(p); n != nil {
					out = append(out, n)
				}
			case *syntax.ProcSubst:
				if n := parseProcSubst(p); n != nil {
					out = append(out, n)
				}
			}
		}
	}
	visit(w.Parts)
	return out
}
