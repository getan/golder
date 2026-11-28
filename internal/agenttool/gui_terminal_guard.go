package agenttool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

// GUITerminalEnvelope carries the actual launch argv and policy ceiling of one
// Agent-created GUI terminal. Cleanup belongs to the process and runs on exit.
type GUITerminalEnvelope struct {
	Argv []string
	// Env, when non-nil, replaces the child's environment. The Windows
	// unconfined fallback uses it to keep provider tokens out of the pane.
	Env      []string
	Cleanup  func()
	Isolated bool
	Network  bool
	Shell    bool
	Policy   [32]byte
}

// PrepareGUITerminal uses the bounded ControlGuard launcher. A one-call path
// grant cannot be turned into a persistent shell capability. On Windows, where
// no OS sandbox exists, the GUI terminal gets an unconfined launch with a
// scrubbed environment; tuios still fails closed.
func (g *ControlGuard) PrepareGUITerminal(ctx context.Context, argv []string, network bool) (GUITerminalEnvelope, error) {
	if g == nil || !agentcore.ExternalToolAuthorized(ctx) {
		return GUITerminalEnvelope{}, errors.New("terminal: an authorized tool call is required")
	}
	cwd, err := g.cwd("")
	if err != nil {
		return GUITerminalEnvelope{}, err
	}
	if filepath.Clean(cwd) != filepath.Clean(g.Root) {
		return GUITerminalEnvelope{}, errors.New("terminal: project identity changed before launch")
	}
	policy := controlPolicyStamp()
	wrapped, cleanup, isolated, err := g.launchArgv(ctx, argv, cwd, network)
	if err != nil {
		return GUITerminalEnvelope{}, err
	}
	if policy != controlPolicyStamp() {
		if cleanup != nil {
			cleanup()
		}
		return GUITerminalEnvelope{}, errors.New("terminal: sandbox policy changed during launch; create a new terminal")
	}
	// An unconfined full-access shell has network even if the request said off;
	// record the actual ceiling rather than claiming a restriction it lacks.
	var env []string
	if !isolated && runtime.GOOS == "windows" {
		env = windowsPaneEnv()
	}
	return GUITerminalEnvelope{Argv: wrapped, Env: env, Cleanup: cleanup, Isolated: isolated, Network: network || !isolated, Shell: len(argv) == 0, Policy: policy}, nil
}

// CheckGUITerminalInput revalidates a persistent envelope immediately before
// input, including live mode and policy changes since the pane was created.
func (g *ControlGuard) CheckGUITerminalInput(ctx context.Context, envelope GUITerminalEnvelope) error {
	return g.checkGUITerminal(ctx, envelope, true)
}

// CheckGUITerminalControl checks layout/lifecycle actions without treating
// them as network input to an existing pane.
func (g *ControlGuard) CheckGUITerminalControl(ctx context.Context, envelope GUITerminalEnvelope) error {
	return g.checkGUITerminal(ctx, envelope, false)
}

func (g *ControlGuard) checkGUITerminal(ctx context.Context, envelope GUITerminalEnvelope, input bool) error {
	if g == nil || !agentcore.ExternalToolAuthorized(ctx) {
		return errors.New("terminal: an authorized tool call is required")
	}
	if g.Mode != nil && g.Mode.Mode() == permissions.ReadOnly {
		return errors.New("terminal: read-only mode cannot send input")
	}
	if envelope.Policy != controlPolicyStamp() {
		return errors.New("terminal: sandbox policy changed; create a new Agent terminal")
	}
	windowsUnconfined := runtime.GOOS == "windows" && !envelope.Isolated
	if input && envelope.Network && !g.network(ctx) && !windowsUnconfined {
		return errors.New("terminal: this pane has network access; input requires reviewed network authority")
	}
	if !envelope.Isolated && (agentcore.SandboxRequestedFromContext(ctx) || seatbelt.ModeFromEnv() == seatbelt.ModeEnforce) {
		return errors.New("terminal: this pane cannot satisfy required OS containment")
	}
	if !envelope.Isolated && !windowsUnconfined && !g.full() {
		return errors.New("terminal: this Agent pane was created outside the sandbox and is no longer in full-access mode")
	}
	return nil
}

// windowsPaneEnv is the allow list a GUI terminal child receives on Windows
// when no OS sandbox exists. The process launcher uses this slice as the whole
// environment, so provider tokens and other daemon secrets cannot leak into an
// Agent-created shell. The variables retained are the ones Windows itself,
// Git Bash, PowerShell and ordinary CLI tools need to start.
func windowsPaneEnv() []string {
	names := []string{
		"PATH", "PATHEXT", "SystemRoot", "SystemDrive", "windir", "ComSpec",
		"TEMP", "TMP", "TMPDIR", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "HOME",
		"APPDATA", "LOCALAPPDATA", "ProgramFiles", "ProgramFiles(x86)",
		"ProgramData", "PSModulePath", "NUMBER_OF_PROCESSORS",
		"PROCESSOR_ARCHITECTURE", "OS", "LANG", "LC_ALL", "LC_CTYPE",
	}
	env := make([]string, 0, len(names))
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			env = append(env, name+"="+value)
		}
	}
	// cmd.exe and PowerShell need a system root even when the daemon was
	// started with a minimal environment.
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
		env = append(env, `SystemRoot=C:\Windows`)
	}
	if os.Getenv("ComSpec") == "" {
		env = append(env, `ComSpec=`+systemRoot+`\System32\cmd.exe`)
	}
	if os.Getenv("TEMP") == "" && os.Getenv("TMP") == "" {
		temp := os.TempDir()
		env = append(env, "TEMP="+temp, "TMP="+temp)
	}
	// Keep the manifest deterministic for tests and logs.
	sort.Slice(env, func(i, j int) bool {
		return strings.ToUpper(env[i]) < strings.ToUpper(env[j])
	})
	return env
}
