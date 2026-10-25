package workspacecmd

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestParseCommands pins the subcommand surface: no word means "start", and
// "run" means exactly the same thing, because "golder workspace" is the command
// someone types and "golder workspace run" is the same command spelled out.
func TestParseCommands(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want Command
	}{
		{"no word starts a workspace", nil, CommandRun},
		{"run", []string{"run"}, CommandRun},
		{"status", []string{"status"}, CommandStatus},
		{"stop", []string{"stop"}, CommandStop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parse(tc.args)
			if err != nil {
				t.Fatalf("parse(%q): %v", tc.args, err)
			}
			if p.command != tc.want {
				t.Errorf("command = %q, want %q", p.command, tc.want)
			}
		})
	}
}

// TestParseFlags pins each flag's spelling against the long and short forms the
// help text documents. A flag that is documented and not parsed is a command
// that silently ignores what it was told.
func TestParseFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want func(parsed) bool
		why  string
	}{
		{
			name: "--cwd separated",
			args: []string{"--cwd", "/w"},
			want: func(p parsed) bool { return p.cwd == "/w" },
			why:  "cwd",
		},
		{
			name: "--cwd=inline",
			args: []string{"--cwd=/w"},
			want: func(p parsed) bool { return p.cwd == "/w" },
			why:  "cwd",
		},
		{
			name: "-C separated",
			args: []string{"-C", "/w"},
			want: func(p parsed) bool { return p.cwd == "/w" },
			why:  "cwd",
		},
		{
			name: "-C=inline",
			args: []string{"-C=/w"},
			want: func(p parsed) bool { return p.cwd == "/w" },
			why:  "cwd",
		},
		{
			name: "-Cattached, the way git spells it",
			args: []string{"-C/w"},
			want: func(p parsed) bool { return p.cwd == "/w" },
			why:  "cwd",
		},
		{
			name: "--session",
			args: []string{"--session", "work"},
			want: func(p parsed) bool { return p.session == "work" },
			why:  "session",
		},
		{
			name: "--socket",
			args: []string{"--socket=/run/x.sock"},
			want: func(p parsed) bool { return p.socket == "/run/x.sock" },
			why:  "socket",
		},
		{
			name: "--config",
			args: []string{"--config", "cfg.toml"},
			want: func(p parsed) bool { return p.config == "cfg.toml" },
			why:  "config",
		},
		{
			name: "--state-dir",
			args: []string{"--state-dir", "st"},
			want: func(p parsed) bool { return p.stateDir == "st" },
			why:  "state dir",
		},
		{
			name: "--theme",
			args: []string{"--theme", "dracula"},
			want: func(p parsed) bool { return p.appear.Theme == "dracula" },
			why:  "theme",
		},
		{
			name: "--headless",
			args: []string{"--headless"},
			want: func(p parsed) bool { return p.headless },
			why:  "headless",
		},
		{
			name: "--headless=false",
			args: []string{"--headless=false"},
			want: func(p parsed) bool { return !p.headless },
			why:  "headless false",
		},
		{
			name: "--shell",
			args: []string{"--shell"},
			want: func(p parsed) bool { return p.shell },
			why:  "shell",
		},
		{
			name: "--nested",
			args: []string{"--nested"},
			want: func(p parsed) bool { return p.nested },
			why:  "nested",
		},
		{
			name: "--ascii-only",
			args: []string{"--ascii-only"},
			want: func(p parsed) bool { return p.appear.ASCIIOnly },
			why:  "ascii",
		},
		{
			name: "--no-animations",
			args: []string{"--no-animations"},
			want: func(p parsed) bool { return p.appear.NoAnimations },
			why:  "animations",
		},
		{
			name: "flags may follow the subcommand",
			args: []string{"status", "--session", "work"},
			want: func(p parsed) bool { return p.command == CommandStatus && p.session == "work" },
			why:  "flag after subcommand",
		},
		{
			name: "flags may precede the subcommand",
			args: []string{"--session", "work", "status"},
			want: func(p parsed) bool { return p.command == CommandStatus && p.session == "work" },
			why:  "flag before subcommand",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parse(tc.args)
			if err != nil {
				t.Fatalf("parse(%q): %v", tc.args, err)
			}
			if !tc.want(p) {
				t.Errorf("parse(%q) did not set the %s: %+v", tc.args, tc.why, p)
			}
		})
	}
}

// TestParseAgentArgsAreArgv is the passthrough contract, pinned literally.
//
// Everything after -- is handed on as tokens. Nothing is joined into a command
// line, nothing is re-split, and nothing is interpreted — not a semicolon, not
// a substitution, not an empty argument, not a word that looks like one of this
// command's own flags. This is what makes `golder workspace -- --model opus`
// mean two arguments rather than one string, and it is the difference between
// forwarding argv and building a shell command out of a person's input.
func TestParseAgentArgsAreArgv(t *testing.T) {
	args := []string{
		"run",
		"--headless",
		"--",
		"--model", "opus",
		"a; rm -rf b",
		"$(id)",
		"`id`",
		"it's",
		"a b  c",
		"line1\nline2",
		"",
		"--headless",
		"--cwd=/elsewhere",
	}
	want := args[3:]

	p, err := parse(args)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(p.agentArgs, want) {
		t.Fatalf("agent argv mismatch\n got: %q\nwant: %q", p.agentArgs, want)
	}
	// The flags after -- did not leak into this command's own settings.
	if !p.headless {
		t.Error("--headless before -- should have been parsed")
	}
	if p.cwd != "" {
		t.Errorf("cwd = %q, and --cwd=/elsewhere came after -- so it belongs to the agent", p.cwd)
	}
}

// TestParseAgentArgsAreCopied: the caller's slice is not aliased, so a driver
// that reuses its argv buffer cannot rewrite what a backend was handed.
func TestParseAgentArgsAreCopied(t *testing.T) {
	args := []string{"--", "one", "two"}
	p, err := parse(args)
	if err != nil {
		t.Fatal(err)
	}
	args[1] = "changed"
	if p.agentArgs[0] != "one" {
		t.Errorf("agentArgs = %q, and the caller's mutation reached it", p.agentArgs)
	}
}

// TestParseRepeatedFlag documents the one thing a repeated flag does: the last
// spelling wins, as it does in every other command here.
func TestParseRepeatedFlag(t *testing.T) {
	p, err := parse([]string{"--session", "one", "--session", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if p.session != "two" {
		t.Errorf("session = %q, want the last one, two", p.session)
	}
}

// TestParseUsageErrors pins the errors: each one is argv this command cannot
// act on, and each message says which word was wrong and what would work.
// They all map to exit 2 (see TestUsageErrorsExitTwo).
func TestParseUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // a substring the message must carry
	}{
		{name: "unknown flag", args: []string{"--bogus"}, want: "--bogus"},
		{name: "unknown short flag", args: []string{"-x"}, want: "-x"},
		{name: "unknown command", args: []string{"wat"}, want: "unknown command"},
		{name: "missing value separated", args: []string{"--session"}, want: "needs a value"},
		{name: "missing value inline", args: []string{"--session="}, want: "needs a value"},
		{name: "missing cwd value", args: []string{"--cwd"}, want: "needs a value"},
		{name: "flag value that is nothing but spaces", args: []string{"--theme", "   "}, want: "needs a value"},
		{name: "bad boolean", args: []string{"--nested=maybe"}, want: "true or false"},
		{name: "too many targets", args: []string{"status", "work", "extra"}, want: "unexpected argument"},
		{name: "positional after subcommand", args: []string{"run", "extra"}, want: "unexpected argument"},
		{name: "agent args on status", args: []string{"status", "--", "x"}, want: "no agent arguments"},
		{name: "agent args on stop", args: []string{"stop", "--", "x"}, want: "no agent arguments"},
		{name: "headless on status", args: []string{"status", "--headless"}, want: "--headless"},
		{name: "shell on stop", args: []string{"stop", "--shell"}, want: "--shell"},
		{name: "shell with agent args", args: []string{"--shell", "--", "x"}, want: "--shell starts no agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(tc.args)
			if err == nil {
				t.Fatalf("parse(%q) = nil error, want a usage error", tc.args)
			}
			var ue *usageError
			if !errors.As(err, &ue) {
				t.Fatalf("parse(%q) error %v is not a usage error", tc.args, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestParseHelpAppearsAnywhere: asking how the command works is never a usage
// error, and never starts anything.
func TestParseHelpAppearsAnywhere(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"status", "--help"}, {"--headless", "help"}} {
		p, err := parse(args)
		if err != nil {
			t.Errorf("parse(%q): %v", args, err)
			continue
		}
		if !p.help {
			t.Errorf("parse(%q) did not report help", args)
		}
	}
}

// TestParseEmptyPassthrough: a bare -- promises an empty argv, and promises it
// without inventing an argument.
func TestParseEmptyPassthrough(t *testing.T) {
	p, err := parse([]string{"--"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.agentArgs) != 0 {
		t.Errorf("agentArgs = %q, want none", p.agentArgs)
	}
	if p.command != CommandRun {
		t.Errorf("command = %q, want run", p.command)
	}
}

// TestSanitizeName pins the folding rule used for default session and socket
// names: it may lose information, so it must never produce a path separator.
func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"api", "api"},
		{"my project", "my-project"},
		{"a/b", "a-b"},
		{"..", ".."},
		{"__x__", "__x__"},
		{"a  b", "a-b"},
		{"-lead-", "lead"},
		{"v1.2.3", "v1.2.3"},
	}
	for _, tc := range cases {
		if got := sanitizeName(tc.in); got != tc.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestUsageDocumentsEveryFlag is the drift guard between the help text and the
// parser: a flag a person can type and cannot find in `--help` is a flag they
// will never type, and a flag in `--help` that nothing parses is a lie.
func TestUsageDocumentsEveryFlag(t *testing.T) {
	flags := []string{
		"--cwd", "-C", "--session", "--socket", "--headless", "--shell",
		"--nested", "--config", "--state-dir", "--ascii-only",
		"--no-animations", "--theme", "--help",
	}
	for _, flag := range flags {
		if !strings.Contains(usage, flag) {
			t.Errorf("the help text does not document %s", flag)
		}
		// And it is a flag the parser accepts, one way or another: either it is
		// a boolean, a value, or help — anything but an unknown flag.
		if _, err := parse(singleFlagArgs(flag)); err != nil {
			t.Errorf("the help documents %s, and the parser refuses it: %v", flag, err)
		}
	}
}

// singleFlagArgs spells one documented flag the way a test needs to parse it:
// a boolean or help flag stands alone, and a value flag is given a value.
func singleFlagArgs(flag string) []string {
	switch flag {
	case "--cwd", "-C":
		return []string{flag, "."}
	case "--session":
		return []string{flag, "work"}
	case "--socket":
		return []string{flag, "ws.sock"}
	case "--config":
		return []string{flag, "c.toml"}
	case "--state-dir":
		return []string{flag, "state"}
	case "--theme":
		return []string{flag, "dark"}
	}
	return []string{flag}
}
