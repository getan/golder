package workspacecmd

import (
	"fmt"
	"strconv"
	"strings"
)

// The argv surface.
//
// One pass, left to right, by hand: the top-level pflag set in cmd/golder
// belongs to the agent's own flags, and an early-dispatch subcommand that
// borrowed it would inherit a hundred flags it does not have (and would parse
// the agent's argv as its own). The other early-dispatch subcommands parse
// this way too.
//
// Two rules make the surface predictable, and both are the reason for the
// "--" convention:
//
//   - Every word before "--" is this command's. An unknown flag, a flag with no
//     value, or a second positional word is argv this command cannot act on,
//     and it says so with exit 2 rather than guessing.
//   - Every word after "--" is the agent's, verbatim: not one of them is
//     inspected, even if it looks like one of this command's flags. That is
//     what lets `golder workspace -- --headless` mean "the agent's --headless"
//     rather than a second meaning for the same word.

// parsed is one `golder workspace` argv, before anything on the machine is
// read. Every field is either absent or exactly what the person typed; nothing
// here consults the environment, the working directory or the filesystem.
type parsed struct {
	command  Command
	help     bool
	cwd      string
	session  string
	name     string
	legacy   string
	socket   string
	config   string
	stateDir string
	headless bool
	shell    bool
	nested   bool
	json     bool
	appear   Appearance
	// agentArgs is the argv after "--": the golder agent's own arguments. It
	// is a copy, so a later mutation of the caller's slice cannot change what
	// the backend is handed.
	agentArgs []string
}

// usageError is argv this command cannot act on. It is a distinct type because
// it is the one error class that maps to exit 2 and points at the help.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// usagef builds one usage error.
func usagef(format string, a ...any) error { return &usageError{msg: fmt.Sprintf(format, a...)} }

// parse reads args — the words after `workspace` in argv — into a parsed.
//
// The grammar is small enough to state:
//
//	argv      := [command [name]] {flag} ["--" {agent arg}]
//	command   := "run" | "new" | "attach" | "list" | "status" | "stop" | "rm" | "help"
//	flag      := "--" name ["=" value] | value-separated forms, "-C" included
//
// Flags may come before or after the subcommand, so `golder workspace status
// --session x` and `golder workspace --session x status` are the same
// invocation. A subcommand that names one session accepts that name as its one
// positional word; every other extra word is an error.
func parse(args []string) (parsed, error) {
	p := parsed{command: CommandRun}
	commandSeen := false

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			// Everything from here on belongs to the agent. It is copied, not
			// aliased: a caller that reuses its argv slice must not be able to
			// rewrite what a backend was handed.
			p.agentArgs = append([]string(nil), args[i+1:]...)
			break
		}

		if !isFlag(arg) {
			if !commandSeen {
				command, err := commandWord(arg)
				if err != nil {
					return p, err
				}
				if command == "" {
					// help is not a command: it may appear anywhere, and it wins.
					p.help = true
					continue
				}
				p.command, commandSeen = command, true
				continue
			}
			if takesName(p.command) && p.name == "" {
				p.name = arg
				continue
			}
			return p, usagef("unexpected argument %q: %q takes no more positional words (agent arguments must follow --)",
				arg, p.command)
		}

		name, value, hasValue, consumed, err := splitFlag(args, i)
		if err != nil {
			return p, err
		}
		i += consumed

		switch name {
		case "--cwd", "-C":
			if p.cwd, err = valueOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--session", "-s":
			if p.session, err = valueOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--legacy":
			if p.legacy, err = valueOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--socket":
			if p.socket, err = valueOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--config":
			if p.config, err = valueOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--state-dir":
			if p.stateDir, err = valueOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--theme":
			if p.appear.Theme, err = valueOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--headless":
			if p.headless, err = boolOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--shell":
			if p.shell, err = boolOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--nested":
			if p.nested, err = boolOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--ascii-only":
			if p.appear.ASCIIOnly, err = boolOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--no-animations":
			if p.appear.NoAnimations, err = boolOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "--json":
			if p.json, err = boolOf(name, value, hasValue); err != nil {
				return p, err
			}
		case "-h", "--help":
			p.help = true
		default:
			return p, usagef("unknown flag %q (run 'golder workspace --help' for the flags this command takes; agent arguments follow --)", name)
		}
	}

	if p.session == "" {
		p.session = p.name
	}
	if p.name != "" && p.session != p.name {
		return p, usagef("the session is named twice, as %q and %q; use one spelling", p.name, p.session)
	}
	return p, p.validate()
}

// validate refuses combinations that have no meaning, rather than ignoring
// them. A flag that is quietly dropped is a person's intent quietly dropped:
// "--headless" on a status command, or agent arguments on a start that runs no
// agent, should say so.
func (p parsed) validate() error {
	if p.help {
		return nil
	}
	if p.legacy != "" {
		switch p.command {
		case CommandAttach, CommandStatus, CommandStop:
		default:
			return usagef("--legacy addresses an old workspace session; it works with attach, status and stop, not %s", p.command)
		}
		if p.session != "" {
			return usagef("--legacy names the old session in its own value; drop --session %q", p.session)
		}
		for flag, given := range map[string]bool{
			"--cwd": p.cwd != "", "--socket": p.socket != "", "--config": p.config != "",
			"--state-dir": p.stateDir != "", "--headless": p.headless, "--shell": p.shell,
			"--nested": p.nested && p.command != CommandAttach, "--json": p.json,
		} {
			if given {
				return usagef("%s does not apply to --legacy sessions", flag)
			}
		}
		if len(p.agentArgs) > 0 {
			return usagef("--legacy attach takes no agent arguments")
		}
		return nil
	}
	if p.json && p.command != CommandList {
		return usagef("--json is only meaningful on 'golder workspace list'")
	}
	if p.command == CommandList && p.session != "" {
		return usagef("'golder workspace list' lists every session; it takes no session name")
	}
	if p.command == CommandNew && p.session == "" {
		return usagef("golder workspace new needs a session name (new <name> or -s <name>)")
	}
	if p.command == CommandRun || p.command == CommandNew {
		if p.shell && len(p.agentArgs) > 0 {
			return usagef("--shell starts no agent, so there is nothing to give the arguments after -- to; drop --shell to run the agent with them")
		}
		return nil
	}
	// Attach may use the appearance flags, because it draws the workspace, and
	// nothing else from a start: no directory, no daemon shape, no agent argv.
	switch {
	case len(p.agentArgs) > 0:
		return usagef("golder workspace %s takes no agent arguments: the words after -- belong to a workspace start", p.command)
	case p.shell:
		return usagef("--shell starts a workspace with a shell pane and no agent; golder workspace %s starts nothing", p.command)
	case p.headless:
		return usagef("golder workspace %s already runs without attaching, so --headless would mean nothing", p.command)
	case p.cwd != "":
		return usagef("-C/--cwd selects the start directory of a new session; it does not apply to %s", p.command)
	case p.socket != "":
		return usagef("--socket is recorded with a new session; golder workspace %s uses the recorded endpoint", p.command)
	case p.config != "":
		return usagef("--config is recorded with a new session; golder workspace %s uses the recorded config", p.command)
	case p.stateDir != "":
		return usagef("--state-dir is recorded with a new session; golder workspace %s uses the recorded state", p.command)
	case p.nested && p.command != CommandAttach:
		return usagef("--nested applies to starting a workspace, not %s", p.command)
	}
	if p.command != CommandAttach && (p.appear.ASCIIOnly || p.appear.NoAnimations || p.appear.Theme != "") {
		return usagef("the appearance flags draw a workspace window; they apply to run, new and attach, not %s", p.command)
	}
	return nil
}

// isFlag reports whether arg is a flag rather than a positional word. A lone
// "-" is not a flag: it is the conventional name for stdin, and a person who
// typed it meant something.
func isFlag(arg string) bool {
	return len(arg) > 1 && arg[0] == '-'
}

// commandWord maps one positional word to the command it names. An empty
// Command with a nil error is "help", which is not a command but is a word this
// command accepts anywhere.
func commandWord(word string) (Command, error) {
	switch word {
	case "run":
		return CommandRun, nil
	case "new":
		return CommandNew, nil
	case "attach":
		return CommandAttach, nil
	case "list":
		return CommandList, nil
	case "status":
		return CommandStatus, nil
	case "stop":
		return CommandStop, nil
	case "rm":
		return CommandRemove, nil
	case "help":
		return "", nil
	}
	return "", usagef("unknown command %q (want run, new, attach, list, status, stop, rm or help)", word)
}

// takesName reports whether a command may be given a session name as its one
// positional word. list names no session; run takes one only through -s so
// `golder workspace run` stays a start and never looks like a session lookup.
func takesName(command Command) bool {
	switch command {
	case CommandNew, CommandAttach, CommandStatus, CommandStop, CommandRemove:
		return true
	}
	return false
}

// splitFlag reads one flag at args[i]: its name, its value when it was written
// inline or as the next word, whether there was one, and how many extra argv
// words it consumed.
//
// Three spellings are accepted, because all three are in use in this codebase's
// other commands: --name value, --name=value, and for the one short flag -C
// value, -C=value and git's -Cvalue. A value that begins with "-" is accepted
// as the next word: refusing to open a directory called "-drafts" would be
// arbitrary, and the separated spelling is unambiguous by position.
func splitFlag(args []string, i int) (name, value string, hasValue bool, consumed int, err error) {
	arg := args[i]
	name = arg

	if eq := strings.IndexByte(arg, '='); eq >= 0 {
		return arg[:eq], arg[eq+1:], true, 0, nil
	}
	// The short flag carries its value with no separator, the way git's -C does.
	if strings.HasPrefix(arg, "-C") && !strings.HasPrefix(arg, "--") && len(arg) > 2 {
		return "-C", arg[2:], true, 0, nil
	}
	if !wantsValue(name) {
		return name, "", false, 0, nil
	}
	if i+1 >= len(args) {
		return name, "", false, 0, usagef("%s needs a value", name)
	}
	return name, args[i+1], true, 1, nil
}

// wantsValue reports whether the flag takes a value, so a missing one is
// reported as "needs a value" against the flag that wanted it rather than
// against whatever word came next.
func wantsValue(name string) bool {
	switch name {
	case "--cwd", "-C", "--session", "-s", "--legacy", "--socket", "--config", "--state-dir", "--theme":
		return true
	}
	return false
}

// valueOf returns the value of a flag that takes one, trimmed, and refuses an
// empty one: "--cwd=”" is the same mistake as "--cwd" with nothing after it,
// and an empty session name or socket path would be a silent fallback to a
// default the person thought they had overridden.
func valueOf(name, value string, hasValue bool) (string, error) {
	value = strings.TrimSpace(value)
	if !hasValue || value == "" {
		return "", usagef("%s needs a value", name)
	}
	return value, nil
}

// boolOf reads a boolean flag. A bare flag is true; "=true" and "=1" spell it
// out for scripts, and "=false" is how a wrapper turns one off without editing
// its own argv.
func boolOf(name, value string, hasValue bool) (bool, error) {
	if !hasValue {
		return true, nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, usagef("%s takes true or false, not %q", name, value)
	}
	return b, nil
}
