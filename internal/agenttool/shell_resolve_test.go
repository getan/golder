package agenttool

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

const (
	testProgramFiles = `C:\Program Files`
	testPowerShell   = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	testCmd          = `C:\Windows\System32\cmd.exe`
)

// stubLookPath turns an exact-name map into the injected LookPath. Windows
// candidate paths are passed back verbatim so the test exercises the same
// absolute-path lookups the real resolver performs.
func stubLookPath(files map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := files[name]; ok {
			return p, nil
		}
		for key, p := range files {
			if strings.EqualFold(key, name) {
				return p, nil
			}
			// exec.LookPath succeeds when given an absolute path to a real
			// file. The resolver re-checks every candidate that way, so the
			// stub must recognize the resolved paths too.
			if strings.EqualFold(p, name) {
				return p, nil
			}
		}
		return "", fmt.Errorf("%s: not found", name)
	}
}

func clearWindowsShellEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ProgramFiles", "ProgramFiles(x86)", "LocalAppData", "UserProfile",
		"ProgramData", "SystemRoot", "ComSpec",
	} {
		t.Setenv(name, "")
	}
}

func stubShellProbe(t *testing.T, probe func(shellSpec) bool) {
	t.Helper()
	old := shellProbe
	shellProbe = probe
	shellProbeCache = sync.Map{}
	t.Cleanup(func() {
		shellProbe = old
		shellProbeCache = sync.Map{}
	})
}

// TestResolveShellWindows covers the measured failure mode: System32\bash.exe
// is the WSL launcher stub, not a usable bash. It must never be selected, and
// a Git Bash that exists but cannot run must fall through to PowerShell.
func TestResolveShellWindows(t *testing.T) {
	gitBash := `C:\Program Files\Git\bin\bash.exe`
	wslStub := `C:\Windows\System32\bash.exe`

	tests := []struct {
		name       string
		env        map[string]string
		files      map[string]string
		probe      func(shellSpec) bool
		wantPath   string
		wantArg    string
		wantNoArg  string
		wantPrefix string
	}{
		{
			name:       "wsl stub is skipped and powershell wins",
			files:      map[string]string{"bash": wslStub, "powershell": testPowerShell},
			wantPath:   testPowerShell,
			wantArg:    "-Command",
			wantNoArg:  "-c",
			wantPrefix: testPowerShell,
		},
		{
			name:       "git bash on path wins",
			files:      map[string]string{"bash": gitBash, "powershell": testPowerShell},
			probe:      func(shellSpec) bool { return true },
			wantPath:   gitBash,
			wantArg:    "-c",
			wantPrefix: gitBash,
		},
		{
			name:       "standard git install discovered without path bash",
			env:        map[string]string{"ProgramFiles": testProgramFiles, "SystemRoot": `C:\Windows`},
			files:      map[string]string{gitBash: gitBash, "powershell": testPowerShell},
			probe:      func(shellSpec) bool { return true },
			wantPath:   gitBash,
			wantArg:    "-c",
			wantPrefix: gitBash,
		},
		{
			name:       "broken git bash falls through to powershell",
			env:        map[string]string{"ProgramFiles": testProgramFiles, "SystemRoot": `C:\Windows`},
			files:      map[string]string{gitBash: gitBash, "powershell": testPowerShell},
			probe:      func(shellSpec) bool { return false },
			wantPath:   testPowerShell,
			wantArg:    "-Command",
			wantNoArg:  "-c",
			wantPrefix: testPowerShell,
		},
		{
			name:       "cmd is the final fallback",
			files:      map[string]string{"cmd": testCmd},
			wantPath:   testCmd,
			wantArg:    "/c",
			wantPrefix: testCmd,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearWindowsShellEnv(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			probe := tc.probe
			if probe == nil {
				probe = func(shellSpec) bool { return false }
			}
			stubShellProbe(t, probe)

			shell := resolveShell("", "windows", stubLookPath(tc.files))
			if shell.path != tc.wantPath {
				t.Fatalf("path = %q, want %q (candidates resolved from %+v)", shell.path, tc.wantPath, tc.files)
			}
			if !containsString(shell.args, tc.wantArg) {
				t.Errorf("args = %q, want to contain %q", shell.args, tc.wantArg)
			}
			if tc.wantNoArg != "" && containsString(shell.args, tc.wantNoArg) {
				t.Errorf("args = %q, must not contain %q", shell.args, tc.wantNoArg)
			}
			if !strings.HasPrefix(shell.path, tc.wantPrefix) {
				t.Errorf("path = %q, want prefix %q", shell.path, tc.wantPrefix)
			}
		})
	}
}

// TestResolveShellExplicitAndPOSIX keeps the configured-shell and Unix
// contracts unchanged.
func TestResolveShellExplicitAndPOSIX(t *testing.T) {
	none := func(string) (string, error) { return "", fmt.Errorf("not found") }

	if got := resolveShell("zsh", "windows", none); got.path != "zsh" || !containsString(got.args, "-c") {
		t.Fatalf("explicit windows shell = %+v, want zsh -c", got)
	}
	if got := resolveShell("fish", "linux", none); got.path != "fish" || !containsString(got.args, "-c") {
		t.Fatalf("explicit linux shell = %+v, want fish -c", got)
	}
	if got := resolveShell("", "linux", none); got.path != "bash" || !containsString(got.args, "-c") {
		t.Fatalf("linux shell = %+v, want bash -c", got)
	}
	if got := resolveShell("", "darwin", none); got.path != "bash" || !containsString(got.args, "-c") {
		t.Fatalf("darwin shell = %+v, want bash -c", got)
	}
}

// TestShellCandidatesRejectsWSLStub checks the path rule directly, including
// the WindowsApps alias.
func TestShellCandidatesRejectsWSLStub(t *testing.T) {
	for _, p := range []string{
		`C:\Windows\System32\bash.exe`,
		`C:\Windows\SysWOW64\bash.exe`,
		`C:\Users\me\AppData\Local\Microsoft\WindowsApps\bash.exe`,
	} {
		if !isWindowsWSLBash(p) {
			t.Errorf("isWindowsWSLBash(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\msys64\usr\bin\bash.exe`,
		`C:\Windows\System32\cmd.exe`,
	} {
		if isWindowsWSLBash(p) {
			t.Errorf("isWindowsWSLBash(%q) = true, want false", p)
		}
	}
}

// TestGitBashCandidates covers the layouts produced by the Git for Windows
// installer and common portable builds.
func TestGitBashCandidates(t *testing.T) {
	cases := []struct {
		git  string
		want string
	}{
		{`C:\Program Files\Git\cmd\git.exe`, `C:\Program Files\Git\bin\bash.exe`},
		{`C:\Program Files\Git\mingw64\bin\git.exe`, `C:\Program Files\Git\bin\bash.exe`},
		{`C:\Tools\PortableGit\bin\git.exe`, `C:\Tools\PortableGit\bin\bash.exe`},
	}
	for _, tc := range cases {
		candidates := gitBashCandidates(tc.git)
		if !containsString(candidates, tc.want) {
			t.Errorf("gitBashCandidates(%q) = %q, want %q", tc.git, candidates, tc.want)
		}
	}
}

// TestPowerShellArgvUsesUTF8Prelude verifies both the flag spelling and that
// the command is a single argv element. PowerShell uses -Command; -c is not a
// PowerShell flag.
func TestPowerShellArgvUsesUTF8Prelude(t *testing.T) {
	shell := shellSpec{path: "powershell.exe", args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}, kind: "powershell"}
	argv := shell.argv("echo 中文测试")
	if len(argv) != 6 {
		t.Fatalf("argv length = %d, want 6: %q", len(argv), argv)
	}
	if argv[4] != "-Command" {
		t.Errorf("flag = %q, want -Command", argv[4])
	}
	if !strings.Contains(argv[5], "[Console]::OutputEncoding") || !strings.Contains(argv[5], "echo 中文测试") {
		t.Errorf("command argument lacks the UTF-8 prelude or the user command: %q", argv[5])
	}
}

func TestShellArgvPreviewBoundsLongCommands(t *testing.T) {
	argv := []string{"bash", "-c", strings.Repeat("x", 2000)}
	got := shellArgvPreview(argv)
	if len(got) > 620 {
		t.Fatalf("preview length = %d, want bounded", len(got))
	}
	if !strings.Contains(got, "bash") {
		t.Errorf("preview lost the shell: %q", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
