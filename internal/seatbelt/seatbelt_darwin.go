//go:build darwin

package seatbelt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Available reports whether sandbox-exec is on PATH.
func Available() bool {
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// SeatbeltRunner generates one sandbox-exec profile per command, scoped to
// the project directory. ProjectDir should be the run's working directory;
// TmpDir defaults to os.TempDir when empty.
type SeatbeltRunner struct {
	ProjectDir string
	TmpDir     string
}

// New returns a Runner for projectDir, or nil when sandbox-exec is missing.
func New(projectDir string) Runner {
	if !Available() {
		return nil
	}
	return &SeatbeltRunner{ProjectDir: projectDir}
}

// SandboxArgv implements Runner.
func (r *SeatbeltRunner) SandboxArgv(shell, flag, command, dir string) ([]string, func(), error) {
	profile, err := r.writeProfile()
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.Remove(profile) }
	return []string{"sandbox-exec", "-f", profile, shell, flag, command}, cleanup, nil
}

func (r *SeatbeltRunner) tmpDir() string {
	if r.TmpDir != "" {
		return r.TmpDir
	}
	return os.TempDir()
}

// canonicals returns the path plus its symlink-resolved form (deduped).
// macOS temp dirs live under /var, which is a symlink to /private/var, while
// the sandbox matches on the canonical vnode path — a profile carrying only
// the symlinked form silently denies every write it meant to allow. Carrying
// both keeps the profile correct regardless of which form the caller used.
func canonicals(p string) []string {
	if p == "" {
		return nil
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil || real == p {
		return []string{p}
	}
	return []string{p, real}
}

func quoteAll(paths []string) string {
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, fmt.Sprintf("(subpath %s)", quote(p)))
	}
	return strings.Join(quoted, " ")
}

// writeProfile renders the seatbelt profile with absolute paths and stores
// it under the temp dir. Reads stay broad (toolchains live all over /usr,
// /opt, home); writes are confined to the project and temp dirs; live
// secret material and the trust store are denied even inside otherwise
// writable trees. Network stays open: exfiltration is the judge's job
// (confirm/sandbox tiers), not the filesystem profile's.
func (r *SeatbeltRunner) writeProfile() (string, error) {
	project := r.ProjectDir
	if project == "" {
		var err error
		project, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("seatbelt: resolve project dir: %w", err)
		}
	}
	home, _ := os.UserHomeDir()
	sshDir := filepath.Join(home, ".ssh")
	gpgDir := filepath.Join(home, ".gnupg")
	profile := fmt.Sprintf(`(version 1)
(deny default)
(allow process-exec process-fork)
(allow job-creation)
(allow signal (target self))
(allow mach-lookup)
(allow sysctl-read)
(allow file-read* (subpath "/"))
(allow file-write* %s %s (literal "/dev/null") (literal "/dev/tty"))
(deny file-write* (subpath %q) (subpath %q) (regex #".*trust\.json$"))
(allow network*)
`,
		quoteAll(canonicals(project)), quoteAll(canonicals(r.tmpDir())), quote(sshDir), quote(gpgDir),
	)
	f, err := os.CreateTemp(r.tmpDir(), "golder-sandbox-*.sb")
	if err != nil {
		return "", fmt.Errorf("seatbelt: write profile: %w", err)
	}
	name := f.Name()
	if _, err := f.WriteString(profile); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("seatbelt: write profile: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("seatbelt: write profile: %w", err)
	}
	return name, nil
}

// quote renders an absolute path as a double-quoted profile string.
func quote(p string) string {
	return "\"" + strings.ReplaceAll(p, "\"", "\\\"") + "\""
}
