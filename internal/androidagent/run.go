package androidagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/proc"
)

// runTool runs one allowlisted command in argv form. It is deliberately not
// named bash: Android ships mksh and toybox, not bash, and this tool has no
// pipes, redirections, variables, globbing or command substitution. Keeping the
// surface that small is what makes a shell-like tool defensible without an OS
// sandbox on the device.
type runTool struct {
	root   string
	binDir string
	perms  *permissions.State
}

const (
	runOutputLimit = 64 << 10
	runDefaultTime = 30 * time.Second
	runMaxTime     = 120 * time.Second
	runMaxArgs     = 64
)

// runCommands is the Android catalog. write marks commands whose normal run
// mutates the workspace; read-only mode blocks those before any process starts.
var runCommands = map[string]struct{ write bool }{
	"ls": {}, "cat": {}, "head": {}, "tail": {}, "wc": {}, "stat": {}, "file": {},
	"du": {}, "df": {}, "strings": {}, "xxd": {}, "hexdump": {},
	"grep": {}, "sed": {}, "sort": {}, "uniq": {}, "cut": {}, "tr": {}, "diff": {},
	"tar": {}, "gzip": {}, "gunzip": {}, "sha256sum": {}, "md5sum": {},
	"ps": {}, "env": {}, "printf": {},
	"cp": {write: true}, "mv": {write: true}, "rm": {write: true},
	"mkdir": {write: true}, "touch": {write: true}, "ln": {write: true},
	"chmod": {write: true}, "tee": {write: true},
}

func (t *runTool) Name() string { return "run" }

func (t *runTool) Description() string {
	return "Run one restricted command in the workspace. argv only: no shell, pipes, " +
		"redirection, variables, globbing or command substitution. Allowed commands: " +
		"ls cat head tail wc stat file du df strings xxd hexdump grep sed sort uniq cut tr " +
		"diff tar gzip gunzip sha256sum md5sum ps env printf cp mv rm mkdir touch ln chmod tee. " +
		"Paths must stay inside the workspace. Write commands are blocked in read-only mode. " +
		"For code search and file edits prefer grep/find and apply_patch."
}

func (t *runTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "argv": {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 64,
      "description": "Executable and arguments, e.g. [\"head\", \"-n\", \"40\", \"main.go\"]."},
    "timeout_ms": {"type": "integer", "minimum": 1000, "maximum": 120000}
  },
  "required": ["argv"],
  "additionalProperties": false
}`)
}

func (t *runTool) ExecutionMode() agentcore.ToolExecutionMode {
	// Writes must not race each other or a read of the same path.
	return agentcore.ToolExecutionSequential
}

func (t *runTool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var args struct {
		Argv      []string `json:"argv"`
		TimeoutMS int      `json:"timeout_ms"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return runError("run: invalid arguments"), nil
	}
	if len(args.Argv) == 0 || len(args.Argv) > runMaxArgs {
		return runError("run: argv must contain 1-64 entries"), nil
	}
	name := args.Argv[0]
	if name == "" || strings.ContainsAny(name, `/\`) {
		return runError("run: the executable must be a bare allowlisted command name"), nil
	}
	if _, ok := runCommands[name]; !ok {
		return runError("run: " + name + " is not in the Android allowlist; use a structured tool instead"), nil
	}
	if runWrites(name, args.Argv[1:]) {
		if t.perms != nil && t.perms.Mode() == permissions.ReadOnly {
			return runError("run: write commands are blocked in read-only mode; switch permission mode or use apply_patch"), nil
		}
	}
	if err := validateRunArgs(t.root, name, args.Argv[1:]); err != nil {
		return runError(err.Error()), nil
	}
	exe := filepath.Join(t.binDir, name)
	if info, err := os.Stat(exe); err != nil || info.IsDir() {
		return runError("run: " + name + " is unavailable on this device"), nil
	}
	timeout := runDefaultTime
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
		if timeout < time.Second {
			timeout = time.Second
		}
		if timeout > runMaxTime {
			timeout = runMaxTime
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, exe, args.Argv[1:]...)
	cmd.Dir = t.root
	cmd.Env = []string{
		"PATH=" + t.binDir + ":/system/xbin",
		"HOME=" + t.root,
		"TMPDIR=" + os.TempDir(),
		"LANG=C",
		"LC_ALL=C",
	}
	var out limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()

	text := out.String()
	if out.truncated {
		text += "\n[output truncated at 64 KiB]"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "[timed out after " + timeout.String() + "]"
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if text != "" && !strings.HasSuffix(text, "\n") {
				text += "\n"
			}
			text += fmt.Sprintf("[exit %d]", exit.ExitCode())
		} else if text == "" {
			text = "run: " + err.Error()
		}
	}
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text)},
		IsError: err != nil,
	}, nil
}

func runError(message string) agentcore.AgentToolResult {
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(message)},
		IsError: true,
	}
}

// limitedBuffer drains every write but keeps only the first 64 KiB, so a chatty
// command cannot exhaust the phone's memory or the model's context.
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := runOutputLimit - b.buf.Len(); room > 0 {
		if len(p) > room {
			_, _ = b.buf.Write(p[:room])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func validateRunArgs(root, name string, args []string) error {
	for _, arg := range args {
		if strings.ContainsAny(arg, "\x00\n\r") {
			return errors.New("run: arguments cannot contain NUL or newline")
		}
	}
	switch name {
	case "find":
		for _, arg := range args {
			switch arg {
			case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
				return fmt.Errorf("run: find %s is not allowed; use the grep/find tools", arg)
			}
		}
	case "sed":
		if hasShortFlag(args, 'i') || hasLongFlag(args, "--in-place") {
			return errors.New("run: sed -i is not allowed; use apply_patch")
		}
	case "tar":
		if hasShortFlag(args, 'x') || hasLongFlag(args, "--extract", "--directory", "--to-command") {
			return errors.New("run: tar extraction is not allowed")
		}
	case "sort":
		if hasShortFlag(args, 'o') || hasLongFlag(args, "--output") {
			return errors.New("run: sort -o is not allowed; use apply_patch")
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		if err := checkRunPath(root, arg); err != nil {
			return err
		}
		if (name == "rm" || name == "chmod") && (arg == "." || arg == "./") {
			return errors.New("run: refusing to operate on the workspace root")
		}
	}
	return nil
}

func checkRunPath(root, arg string) error {
	if filepath.IsAbs(arg) {
		return fmt.Errorf("run: absolute path %q is not allowed; stay inside the workspace", arg)
	}
	clean := filepath.Clean(arg)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("run: path %q escapes the workspace", arg)
	}
	full := filepath.Join(root, clean)
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		full = resolved
	} else if resolved, err := filepath.EvalSymlinks(filepath.Dir(full)); err == nil {
		full = filepath.Join(resolved, filepath.Base(full))
	}
	if !withinRoot(root, full) {
		return fmt.Errorf("run: path %q escapes the workspace", arg)
	}
	return nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// runWrites extends the static write flag for commands whose behavior depends
// on flags or extra operands.
func runWrites(name string, args []string) bool {
	switch name {
	case "sed":
		return hasShortFlag(args, 'i') || hasLongFlag(args, "--in-place")
	case "gzip", "gunzip":
		return !hasShortFlag(args, 'c') && !hasShortFlag(args, 'l') && !hasShortFlag(args, 't') &&
			!hasLongFlag(args, "--stdout", "--list", "--test")
	case "tar":
		return hasShortFlag(args, 'c') || hasLongFlag(args, "--create")
	case "sort":
		return hasShortFlag(args, 'o') || hasLongFlag(args, "--output")
	case "uniq":
		operands := 0
		for _, arg := range args {
			if !strings.HasPrefix(arg, "-") {
				operands++
			}
		}
		return operands > 1
	default:
		return runCommands[name].write
	}
}

func hasShortFlag(args []string, flag byte) bool {
	for _, arg := range args {
		if len(arg) > 1 && arg[0] == '-' && arg[1] != '-' {
			if strings.ContainsRune(arg[1:], rune(flag)) {
				return true
			}
		}
	}
	return false
}

func hasLongFlag(args []string, names ...string) bool {
	for _, arg := range args {
		for _, name := range names {
			if arg == name || strings.HasPrefix(arg, name+"=") {
				return true
			}
		}
	}
	return false
}
