package agenttool

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/proc"
)

// shellSpec is one concrete interpreter and the flags that precede the
// command. Keeping the flags separate from the command lets Windows use
// PowerShell's multi-flag invocation without ever building a command line by
// string concatenation.
type shellSpec struct {
	path string
	args []string
	kind string // bash, powershell, cmd, explicit
}

// argv renders the interpreter invocation for one command. Every element is a
// separate argv entry: a working directory or command containing spaces can
// never be re-split by the child process.
func (s shellSpec) argv(command string) []string {
	argv := make([]string, 0, len(s.args)+2)
	argv = append(argv, s.path)
	argv = append(argv, s.args...)
	argv = append(argv, s.commandArg(command))
	return argv
}

func (s shellSpec) commandArg(command string) string {
	if s.kind == "powershell" {
		return powershellUTF8Prelude + command
	}
	return command
}

// powershellUTF8Prelude asks Windows PowerShell to write redirected output as
// UTF-8. The console property setter throws when the process has no console
// (the GUI case), so it is guarded: failing to set it must never fail the
// command itself.
const powershellUTF8Prelude = "try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false } catch {}; $OutputEncoding = New-Object System.Text.UTF8Encoding $false; "

// resolveShell picks the interpreter for one command. An explicit shell is
// always honored. Otherwise Windows gets a real Git Bash when one can be
// started, then PowerShell, then cmd. A POSIX host always gets bash -c.
func resolveShell(explicit, goos string, lookPath func(string) (string, error)) shellSpec {
	if explicit != "" {
		return shellSpec{path: explicit, args: []string{"-c"}, kind: "explicit"}
	}
	candidates := shellCandidates(goos, lookPath)
	for _, candidate := range candidates {
		// Only bash needs a liveness probe. System32\bash.exe is excluded by
		// path, and a Git Bash that cannot even run "exit 0" would otherwise
		// make every command fail with the same opaque startup error.
		if candidate.kind != "bash" || shellUsable(candidate) {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

// shellCandidates returns the ordered fallback chain. The final cmd entry is
// always present so Start can report a concrete executable instead of a
// "no shell" error.
func shellCandidates(goos string, lookPath func(string) (string, error)) []shellSpec {
	if goos != "windows" {
		return []shellSpec{{path: "bash", args: []string{"-c"}, kind: "bash"}}
	}

	var out []shellSpec
	seen := make(map[string]bool)
	add := func(p string, args []string, kind string) {
		p = strings.TrimSpace(p)
		if p == "" || isWindowsWSLBash(p) {
			return
		}
		resolved, err := lookPath(p)
		if err != nil || resolved == "" {
			return
		}
		key := strings.ToLower(normalizeWindowsPath(resolved))
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, shellSpec{path: resolved, args: args, kind: kind})
	}

	// PATH may contain Git Bash or MSYS2. It may also contain the WSL launcher
	// stub, which add() rejects: that stub exits 1 before it ever sees the
	// command.
	if p, err := lookPath("bash"); err == nil {
		add(p, []string{"-c"}, "bash")
	}

	bashArgs := []string{"-c"}
	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")
	localAppData := os.Getenv("LocalAppData")
	userProfile := os.Getenv("UserProfile")
	programData := os.Getenv("ProgramData")

	// Git for Windows installs. The per-user installer is the common case on
	// machines without administrator access.
	for _, base := range []string{
		winJoin(programFiles, "Git"),
		winJoin(programFilesX86, "Git"),
		winJoin(localAppData, "Programs", "Git"),
	} {
		add(winJoin(base, "bin", "bash.exe"), bashArgs, "bash")
		add(winJoin(base, "usr", "bin", "bash.exe"), bashArgs, "bash")
	}
	// Package-manager layouts.
	add(winJoin(userProfile, "scoop", "apps", "git", "current", "bin", "bash.exe"), bashArgs, "bash")
	add(winJoin(programData, "chocolatey", "lib", "git", "tools", "bin", "bash.exe"), bashArgs, "bash")

	// Derive <git>\bin\bash.exe from wherever `git` itself was found. This also
	// covers portable Git installations that are not in a standard location.
	if git, err := lookPath("git"); err == nil {
		for _, candidate := range gitBashCandidates(git) {
			add(candidate, bashArgs, "bash")
		}
	}

	// PowerShell 7 first, then Windows PowerShell 5.1. Use -Command, never -c.
	psArgs := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}
	for _, name := range []string{"pwsh", "pwsh.exe"} {
		if p, err := lookPath(name); err == nil {
			add(p, psArgs, "powershell")
		}
	}
	add(winJoin(programFiles, "PowerShell", "7", "pwsh.exe"), psArgs, "powershell")
	for _, name := range []string{"powershell", "powershell.exe"} {
		if p, err := lookPath(name); err == nil {
			add(p, psArgs, "powershell")
		}
	}
	systemRoot := os.Getenv("SystemRoot")
	add(winJoin(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), psArgs, "powershell")

	// cmd is the final fallback. COMSPEC is authoritative when it is set.
	cmdArgs := []string{"/d", "/s", "/c"}
	add(os.Getenv("ComSpec"), cmdArgs, "cmd")
	add(winJoin(systemRoot, "System32", "cmd.exe"), cmdArgs, "cmd")
	if p, err := lookPath("cmd"); err == nil {
		add(p, cmdArgs, "cmd")
	}

	hasCmd := false
	for _, candidate := range out {
		if candidate.kind == "cmd" {
			hasCmd = true
			break
		}
	}
	if !hasCmd {
		out = append(out, shellSpec{path: "cmd.exe", args: cmdArgs, kind: "cmd"})
	}
	return out
}

// isWindowsWSLBash identifies the WSL launcher stub. It is a console program
// named bash.exe, but without a registered distribution it prints a localized
// message and exits 1 before running the command; when redirected, that
// message is commonly reduced to a row of '?' characters.
func isWindowsWSLBash(p string) bool {
	n := strings.ToLower(normalizeWindowsPath(p))
	if path.Base(n) != "bash.exe" {
		return false
	}
	return strings.HasSuffix(n, "/windows/system32/bash.exe") ||
		strings.HasSuffix(n, "/windows/syswow64/bash.exe") ||
		strings.HasSuffix(n, "/windows/sysnative/bash.exe") ||
		strings.Contains(n, "/microsoft/windowsapps/")
}

// normalizeWindowsPath makes Windows paths comparable regardless of the host
// running the test: both separators become '/', and '.'/'..' are collapsed.
func normalizeWindowsPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, `\`, "/")
	return path.Clean(p)
}

// winJoin builds a Windows path without depending on the host's filepath
// separator, which keeps the Windows resolver testable on macOS and Linux.
func winJoin(base string, elems ...string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	p := strings.TrimRight(base, `\/`)
	for _, elem := range elems {
		elem = strings.Trim(elem, `\/`)
		if elem == "" {
			continue
		}
		p += `\` + elem
	}
	return p
}

// gitBashCandidates maps common `git.exe` layouts to the sibling bash.exe.
func gitBashCandidates(git string) []string {
	norm := normalizeWindowsPath(git)
	lower := strings.ToLower(norm)
	var roots []string
	for _, suffix := range []string{
		"/cmd/git.exe",
		"/bin/git.exe",
		"/mingw64/bin/git.exe",
		"/usr/bin/git.exe",
	} {
		if strings.HasSuffix(lower, suffix) {
			roots = append(roots, strings.ReplaceAll(norm[:len(norm)-len(suffix)], "/", `\`))
		}
	}
	var out []string
	for _, root := range roots {
		out = append(out,
			winJoin(root, "bin", "bash.exe"),
			winJoin(root, "usr", "bin", "bash.exe"))
	}
	return out
}

// shellProbe is a package variable so tests can decide which candidates are
// healthy without launching processes on the test host.
var shellProbe = probeShell

// shellProbeCache memoizes the result for one executable version. Keying on
// size and modification time means a repaired or reinstalled Git Bash is
// probed again rather than staying marked broken for the life of the process.
// A negative result expires quickly so a slow first start (antivirus, a cold
// disk) does not permanently downgrade every later command to PowerShell.
var shellProbeCache sync.Map

type shellProbeResult struct {
	usable  bool
	checked time.Time
}

const (
	shellProbeTimeout     = 5 * time.Second
	shellProbeNegativeTTL = 30 * time.Second
)

func shellUsable(s shellSpec) bool {
	key := shellProbeKey(s.path)
	if cached, ok := shellProbeCache.Load(key); ok {
		if result, ok := cached.(shellProbeResult); ok {
			if result.usable || time.Since(result.checked) < shellProbeNegativeTTL {
				return result.usable
			}
		}
	}
	usable := shellProbe(s)
	shellProbeCache.Store(key, shellProbeResult{usable: usable, checked: time.Now()})
	return usable
}

func shellProbeKey(p string) string {
	info, err := os.Stat(p)
	if err != nil {
		return p
	}
	return fmt.Sprintf("%s|%d|%d", p, info.ModTime().UnixNano(), info.Size())
}

func probeShell(s shellSpec) bool {
	ctx, cancel := context.WithTimeout(context.Background(), shellProbeTimeout)
	defer cancel()
	args := append(append([]string{}, s.args...), "exit 0")
	cmd := proc.CommandContext(ctx, s.path, args...)
	return cmd.Run() == nil
}
