//go:build ignore

// gen_risk_table reads the bash-classify rule table and writes risk_table.go.
//
// It is a generator, not part of the build (hence the ignore constraint): the
// table it produces is data this package ships, and the clone it reads is
// reading material a developer pulls by hand. Run it from the repository root
// after cloning the reference:
//
//	git clone --depth 1 https://github.com/fprochazka/bash-classify .ref-bash-classify
//	go run internal/judge/gen_risk_table.go
//
// Keeping the generator in the tree is what makes the table reproducible: the
// data is MIT-licensed and must be re-derivable, not hand-edited, or the next
// person cannot tell a transcription slip from a deliberate change.
//
// The YAML subset it parses is deliberately tiny — `command:`, top-level
// `classification:`, the `subcommands:` block's names with their classifications,
// and the option overrides that RAISE a classification. Everything else
// (aliases, delegation, the option overrides that LOWER one, option arity) is
// left behind: the raising overrides are the safety-relevant subset — `git push
// --force` is a different act from `git push` — while an override that lowers
// severity only ever makes a rule milder than the source's, which is the
// conservative direction.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	sourceDir = ".ref-bash-classify/src/bash_classify/commands"
	outFile   = "internal/judge/risk_table.go"
)

func main() {
	files, err := filepath.Glob(filepath.Join(sourceDir, "*.yaml"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "gen_risk_table: no rules under %s; clone the reference first (see the file comment)\n", sourceDir)
		os.Exit(1)
	}

	table := map[string]*rule{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if r := parseRule(string(data)); r != nil {
			table[r.name] = r
		}
	}

	out := &strings.Builder{}
	out.WriteString(header)
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	out.WriteString("var riskRules = map[string]commandRule{\n")
	for _, name := range names {
		writeRule(out, "\t", name, table[name])
	}
	out.WriteString("}\n")

	if err := os.WriteFile(outFile, []byte(out.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gen_risk_table: write %s: %v\n", outFile, err)
		os.Exit(1)
	}
	fmt.Printf("gen_risk_table: %d commands -> %s\n", len(table), outFile)
}

// rule is one parsed entry: a command, or (inside subs) one of its subcommands.
type rule struct {
	name  string
	class string
	flags map[string]string
	subs  map[string]*rule
}

// writeRule emits one rule as Go source, recursing into subcommands.
func writeRule(out *strings.Builder, indent, name string, r *rule) {
	subNames := make([]string, 0, len(r.subs))
	for s := range r.subs {
		subNames = append(subNames, s)
	}
	sort.Strings(subNames)
	fmt.Fprintf(out, "%s%q: {class: %s", indent, name, constName(r.class))
	if len(r.flags) > 0 {
		out.WriteString(", flags: map[string]Classification{\n")
		flagNames := make([]string, 0, len(r.flags))
		for f := range r.flags {
			flagNames = append(flagNames, f)
		}
		sort.Strings(flagNames)
		for _, f := range flagNames {
			fmt.Fprintf(out, "%s\t%q: %s,\n", indent, f, constName(r.flags[f]))
		}
		fmt.Fprintf(out, "%s}", indent)
	}
	if len(subNames) > 0 {
		out.WriteString(", subs: map[string]commandRule{\n")
		for _, s := range subNames {
			writeRule(out, indent+"\t", s, r.subs[s])
		}
		fmt.Fprintf(out, "%s}", indent)
	}
	out.WriteString("},\n")
}

// parseRule pulls the command name, its classification, and any subcommand
// classifications out of one rule file.
//
// Indentation is the structure: an unindented key belongs to the command, a
// two-space key inside the subcommands block names a subcommand, and the
// four-space classification under it belongs to that subcommand. Reading it any
// other way silently drops the subcommand tables, which are where the
// interesting answers live (`git status` reads; the `git` base does not).
func parseRule(text string) *rule {
	root := &rule{subs: map[string]*rule{}}
	// The subcommands block nests: `gh run list` is a subcommand of a
	// subcommand, and the source states its classification one level down
	// (`run: UNKNOWN`, `run.list: READONLY`). A one-level reader would keep the
	// parent's answer, which for gh is UNKNOWN — so every `gh run list` would
	// defer to a model. levelRule maps an indentation width to the rule declared
	// at it, which is what makes the nesting depth fall out of the text rather
	// than out of a hard-coded number of levels.
	levelRule := map[int]*rule{}
	inSubs := false
	section := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case indent == 0 && strings.HasPrefix(trimmed, "command:"):
			root.name = trimValue(trimmed)
			inSubs = false
			section = ""
		case indent == 0 && strings.HasPrefix(trimmed, "classification:"):
			root.class = trimValue(trimmed)
		case indent == 0 && strings.HasPrefix(trimmed, "subcommands:"):
			inSubs = true
			section = ""
			levelRule[0] = root
		case indent == 0 && strings.HasPrefix(trimmed, "options:"):
			section = "options"
		case indent == 0 && strings.HasPrefix(trimmed, "global_options:"):
			section = "options"
		case inSubs && strings.HasSuffix(trimmed, ":") && strings.HasPrefix(trimmed, "subcommands:"):
			// A nested subcommands block: the level above it is the parent.
			section = ""
		case inSubs && strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " "):
			if strings.HasPrefix(trimmed, "options:") || strings.HasPrefix(trimmed, "global_options:") {
				section = "sub"
				continue
			}
			name := strings.TrimSuffix(trimmed, ":")
			r := &rule{name: name, subs: map[string]*rule{}}
			levelRule[indent] = r
			// The parent is the nearest rule declared to the LEFT, not necessarily
			// two columns over: `git stash push` is declared at indent 6 under a
			// subcommand at indent 2, and a fixed two-column step put it on the
			// root instead — where it overwrote `git push`, so `git push --force`
			// read as `git stash push` and dropped below the tier that defers it.
			parent := nearestRule(levelRule, indent)
			if parent == nil {
				parent = root
			}
			parent.subs[name] = r
			section = ""
		case inSubs && strings.HasPrefix(trimmed, "classification:"):
			// Belongs to the nearest rule declared to the left.
			if r := nearestRule(levelRule, indent); r != nil {
				r.class = trimValue(trimmed)
			}
		case inSubs && strings.Contains(trimmed, "overrides:"):
			if r := nearestRule(levelRule, indent); r != nil {
				addFlag(r, trimmed)
			} else {
				addFlag(root, trimmed)
			}
		case section == "options" && strings.Contains(trimmed, "overrides:"):
			addFlag(root, trimmed)
		}
	}
	if root.name == "" {
		return nil
	}
	if root.class == "" {
		root.class = "UNKNOWN"
	}
	fillAndPrune(root, root.class)
	return root
}

// nearestRule returns the rule declared at the deepest level above indent, which
// is the block a line at this indentation belongs to.
func nearestRule(levels map[int]*rule, indent int) *rule {
	for i := indent - 2; i >= 0; i -= 2 {
		if r, ok := levels[i]; ok {
			return r
		}
	}
	return nil
}

// fillAndPrune gives every subcommand the classification it inherits when it
// declares none, then drops the flags that do not raise it.
func fillAndPrune(r *rule, inherited string) {
	if r.class == "" {
		r.class = inherited
	}
	pruneFlags(r, r.class)
	for _, sub := range r.subs {
		fillAndPrune(sub, r.class)
	}
	if len(r.flags) == 0 {
		r.flags = nil
	}
	if len(r.subs) == 0 {
		r.subs = nil
	}
}

// addFlag records one option line's override, keeping the flag name (`--force`,
// `-f`) and the classification it raises to.
func addFlag(r *rule, trimmed string) {
	key, rest, ok := strings.Cut(trimmed, ":")
	if !ok {
		return
	}
	idx := strings.Index(rest, "overrides:")
	if idx < 0 {
		return
	}
	class := strings.TrimSpace(rest[idx+len("overrides:"):])
	if end := strings.IndexAny(class, ",}"); end >= 0 {
		class = class[:end]
	}
	class = strings.TrimSpace(strings.Trim(class, `"'`))
	if class == "" {
		return
	}
	flag := strings.TrimSpace(key)
	if !strings.HasPrefix(flag, "-") {
		return
	}
	if r.flags == nil {
		r.flags = map[string]string{}
	}
	r.flags[flag] = class
}

// pruneFlags drops the overrides that do not RAISE the classification: they
// describe how a flag makes a command safer (--help), which this table does not
// need to carry — leaving the base classification in place is the conservative
// reading, and 356 of the source's overrides are of exactly that kind.
func pruneFlags(r *rule, base string) {
	for flag, class := range r.flags {
		if severity(class) <= severity(base) {
			delete(r.flags, flag)
		}
	}
	if len(r.flags) == 0 {
		r.flags = nil
	}
}

func severity(class string) int {
	switch class {
	case "READONLY":
		return 0
	case "LOCAL_EFFECTS":
		return 1
	case "EXTERNAL_EFFECTS":
		return 2
	case "UNKNOWN":
		return 3
	case "DANGEROUS":
		return 4
	}
	return 3
}

func trimValue(line string) string {
	v := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
	v = strings.Trim(v, `"'`)
	return strings.TrimSpace(v)
}

func constName(class string) string {
	switch class {
	case "READONLY":
		return "ReadOnly"
	case "LOCAL_EFFECTS":
		return "LocalEffects"
	case "EXTERNAL_EFFECTS":
		return "ExternalEffects"
	case "DANGEROUS":
		return "Dangerous"
	default:
		return "Unknown"
	}
}

const header = `package judge

// The local rule table: what a program does to the world, for the local
// classifier stage (Local, in local.go).
//
// GENERATED by gen_risk_table.go from bash-classify"s rule files. Derived from
// bash-classify by Filip Prochazka, MIT licensed:
// https://github.com/fprochazka/bash-classify
//
// The data is copied rather than depended on because the runtime dependency would
// be a Python package and a tree-sitter grammar — for a decision this package
// already has the parse for. Only the CLASSIFICATION is taken: the option-level
// overrides, aliases and delegation rules in the source are not, because the
// local stage grades the command and its subcommand and nothing finer. That is
// also why a program"s rule here can only be more conservative than the source"s,
// never less: an option this table does not model cannot lower a classification.
//
// Re-generate with:
//
//	git clone --depth 1 https://github.com/fprochazka/bash-classify .ref-bash-classify
//	go run internal/judge/gen_risk_table.go

// commandRule is one program"s entry: what it does, and what each of its
// subcommands does when that differs.
//
// flags carries the option overrides that RAISE the classification: "git push
// --force" destroys remote work in a way "git push" does not, and the source
// states that at the option. Overrides that lower a classification are not
// carried — see gen_risk_table.go for why that is the safe direction.
type commandRule struct {
	// class is the program"s own classification, used when no subcommand is
	// named or the one named is not listed.
	class Classification
	// flags raises the classification when one of these appears in the argv.
	flags map[string]Classification
	// subs overrides class for a named subcommand: "git status" reads, "git
	// push" does not.
	subs map[string]commandRule
}

`
