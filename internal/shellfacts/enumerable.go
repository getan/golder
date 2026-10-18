package shellfacts

// Enumerable: is everything this command line will touch readable from its
// text?

import "strings"

// The parser already knows what it could not resolve — a variable, a
// substitution, an assignment it cannot expand. Enumerable turns those facts
// into the one yes/no a caller can act on, and it exists because some decisions
// treat the argv as the whole story: which paths a consent question must cover,
// whether a single path can stand for the call, and whether a command may run
// outside the OS sandbox, where nothing else bounds it.
//
// The answer is deliberately conservative, and it is a property of the READING,
// not of the intent:
//
//   - a glob stays enumerable: `src/*.go` is bounded by the directory it is
//     written against, and the caller's question is which place is touched;
//   - a `~` stays enumerable: the shell expands it to a value this process
//     knows (its own home), which is exactly the fact the boundary gate wants;
//   - a `$VAR`, a `$(...)`, an arithmetic expansion or a process substitution
//     does not: the value is a runtime fact, and nothing here may guess it;
//   - an environment assignment that changes what runs or what code is loaded
//     does not: `PATH=…` picks another program, `LD_PRELOAD=/x.so` injects one,
//     `HOME=…` moves the configuration and credentials the same command reads.
//     The test is the VARIABLE'S NAME, not the shape of its value;
//     `GOCACHE=/tmp/cache go test` is a cache directory and stays enumerable;
//   - a construct this layer walked past does not: a loop or a conditional is
//     reported rather than described, and "there is a for loop here" is
//     precisely the fact that says "do not reason about this from the text".
//
// False means "treat this call as one whose effect cannot be bounded from its
// text", never "there is nothing here".
func (p *Plan) Enumerable() (bool, string) {
	if p == nil {
		return false, "there is no parsed command"
	}
	if !p.Parsed {
		if p.ParseError != "" {
			return false, "the line did not parse: " + p.ParseError
		}
		return false, "the line did not parse"
	}
	if len(p.Constructs) > 0 {
		return false, "the line contains " + strings.Join(dedupeStrings(p.Constructs), ", ")
	}
	// AllCommands, not Commands: `echo "$(rm -rf /x)"` has one top-level
	// command and the substitution's commands hang off it. Enumerating the
	// display view would miss the very thing this predicate is about.
	for _, c := range p.AllCommands() {
		if _, literal := c.Program(); !literal {
			return false, "the program itself is computed at runtime"
		}
		for _, w := range c.Argv {
			if why, dynamic := expansionReason(w); dynamic {
				return false, why
			}
		}
		for _, w := range c.Assigns {
			if why, dynamic := expansionReason(w); dynamic {
				return false, why
			}
			if w.Dynamic {
				// A tilde or a glob in an assignment is computed at runtime,
				// and an array assignment is not a single value at all.
				return false, "the assignment " + strings.TrimSpace(w.Text) + " is computed at runtime"
			}
			if why, changes := changesWhatRuns(w.Text); changes {
				return false, "the environment assignment " + w.Text + " changes what runs (" + why + ")"
			}
		}
		for _, r := range c.Redirs {
			if r.Dup {
				continue // `2>&1` names a descriptor, not a path
			}
			if why, dynamic := expansionReason(r.Target); dynamic {
				return false, why
			}
		}
	}
	return true, ""
}

// expansionReason reports the first part of w whose value is decided at run
// time, with a reason a sentence can carry.
func expansionReason(w Word) (string, bool) {
	for _, part := range w.Parts {
		var what string
		switch part.Kind {
		case VariablePart:
			what = "expands a variable"
		case CommandSubstPart:
			what = "runs a command substitution"
		case ArithmPart:
			what = "expands arithmetic"
		case ProcSubstPart:
			what = "creates a process substitution"
		default:
			continue
		}
		text := strings.TrimSpace(w.Text)
		if text == "" {
			return what, true
		}
		return "the word " + text + " " + what, true
	}
	return "", false
}

// changesWhatRuns reports whether `NAME=value` can change the program that runs
// or the code and configuration it loads, with a reason a sentence can carry.
//
// Why a NAME and not the value's shape: the first version of this rule refused
// any assignment whose value contained a slash, on the theory that a path in an
// assignment is a path no argument shows. That is true of the variables below
// and false of everything else — `GOCACHE=/Volumes/cache go test` touches a
// cache directory and nothing more — and the cost of the over-broad test was
// real: the whole command fell out of the envelope (and so out of the
// network-follows-approval path) because of a cache path.
//
// The list is a deny list because its failure mode is containment, and the
// question it answers is narrow: does this assignment change what executes, or
// which configuration and credentials the same command reads?
func changesWhatRuns(assignment string) (string, bool) {
	name, _, ok := strings.Cut(assignment, "=")
	if !ok {
		return "", false
	}
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return "", false
	}
	if why, known := runtimeEnvVars[name]; known {
		return why, true
	}
	for _, family := range runtimeEnvFamilies {
		if strings.HasPrefix(name, family.prefix) {
			return family.why, true
		}
	}
	return "", false
}

// runtimeEnvVars are the variables whose value changes what a command runs or
// what it loads. Each entry is a name this layer can point at, not a guess
// about intent.
var runtimeEnvVars = map[string]string{
	// Program lookup and shell behaviour: the command word is no longer the
	// program that runs.
	"PATH":           "it picks a different program to run",
	"CDPATH":         "it moves where the shell looks for the program",
	"IFS":            "it changes how the shell splits words",
	"GLOBIGNORE":     "it changes which paths a glob matches",
	"BASH_ENV":       "the shell sources the file it names",
	"ENV":            "the shell sources the file it names",
	"SHELLOPTS":      "it turns shell options on",
	"BASHOPTS":       "it turns shell options on",
	"PS4":            "the shell expands it before every traced command",
	"PROMPT_COMMAND": "the shell runs it as a command",
	"ZDOTDIR":        "it moves the shell's startup files",
	"SHELL":          "it selects the shell another program runs",
	// Dynamic loaders: the process loads code from the value.
	"LD_PRELOAD":            "the loader loads code from it",
	"LD_LIBRARY_PATH":       "the loader loads libraries from it",
	"LD_AUDIT":              "the loader loads an audit library from it",
	"DYLD_INSERT_LIBRARIES": "the loader loads code from it",
	"DYLD_LIBRARY_PATH":     "the loader loads libraries from it",
	"DYLD_FRAMEWORK_PATH":   "the loader loads frameworks from it",
	// Interpreters and toolchains read their own search paths and options.
	"PYTHONPATH":        "the interpreter imports from it",
	"PYTHONSTARTUP":     "the interpreter runs the file it names",
	"PYTHONHOME":        "it moves the interpreter's own libraries",
	"PERL5LIB":          "the interpreter loads modules from it",
	"PERL5OPT":          "the interpreter takes options from it",
	"RUBYLIB":           "the interpreter loads code from it",
	"RUBYOPT":           "the interpreter takes options from it",
	"NODE_OPTIONS":      "the runtime takes options from it",
	"NODE_PATH":         "the runtime loads modules from it",
	"JAVA_TOOL_OPTIONS": "the JVM takes options from it",
	"_JAVA_OPTIONS":     "the JVM takes options from it",
	"CLASSPATH":         "the JVM loads classes from it",
	"LUA_PATH":          "the interpreter loads code from it",
	"LUA_CPATH":         "the interpreter loads code from it",
	"GOFLAGS":           "it can pass -toolexec, which runs a program of its choosing",
	"GOTOOLCHAIN":       "it can run a toolchain downloaded on the spot",
	"GOROOT":            "it points the toolchain at another directory",
	"GOENV":             "it points the tool at another config file",
	// Home and configuration: the same command reads different settings and
	// different credentials.
	"HOME":                        "it moves the configuration and credentials the command reads",
	"XDG_CONFIG_HOME":             "it moves the configuration the command reads",
	"NETRC":                       "it points at another credential file",
	"CURL_HOME":                   "it points at another curl configuration",
	"CARGO_HOME":                  "it moves cargo's configuration and registries",
	"RUSTUP_HOME":                 "it moves the toolchain rustup selects",
	"RUSTUP_TOOLCHAIN":            "it selects another toolchain",
	"KUBECONFIG":                  "it points at another cluster configuration",
	"DOCKER_CONFIG":               "it points at another docker configuration",
	"AWS_CONFIG_FILE":             "it points at another AWS configuration",
	"AWS_SHARED_CREDENTIALS_FILE": "it points at another credential file",
	"GNUPGHOME":                   "it moves the keyring gpg uses",
	"PIP_CONFIG_FILE":             "it points pip at another configuration",
	"SSH_AUTH_SOCK":               "it points at another key agent",
	// Git: the same command runs a different helper, hooks from another
	// directory, or reads another repository.
	"GIT_EXEC_PATH":                    "git runs helpers from it",
	"GIT_SSH":                          "git runs the program it names",
	"GIT_SSH_COMMAND":                  "git runs the command it names",
	"GIT_CONFIG":                       "git reads configuration from it",
	"GIT_CONFIG_GLOBAL":                "git reads configuration from it",
	"GIT_CONFIG_SYSTEM":                "git reads configuration from it",
	"GIT_DIR":                          "git works on another repository",
	"GIT_WORK_TREE":                    "git works on another working tree",
	"GIT_INDEX_FILE":                   "git uses another index",
	"GIT_TEMPLATE_DIR":                 "git copies hooks from it",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": "git reads objects from it",
}

// runtimeEnvFamilies are prefixes whose whole family carries the same meaning.
var runtimeEnvFamilies = []struct{ prefix, why string }{
	{"LD_", "the loader reads it"},
	{"DYLD_", "the loader reads it"},
	{"BASH_FUNC_", "it exports a shell function that shadows a command"},
}

// dedupeStrings returns the values once each, in order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
