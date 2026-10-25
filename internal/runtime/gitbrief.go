package runtime

import (
	"os/exec"
	"strings"
)

// gitStatusBrief returns a bounded, read-only snapshot of the working tree for
// a fork/fresh task child. It is best-effort: outside a repository, without
// git, or on a slow filesystem it simply returns "". The child can always run
// `git status` itself; this only saves the first round trip.
func gitStatusBrief(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	out, err := runGitReadOnly(dir, "status", "--porcelain", "-b")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	const maxLines = 40
	var b strings.Builder
	for i, line := range lines {
		if line == "" {
			continue
		}
		if i >= maxLines {
			b.WriteString("… (truncated)\n")
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// runGitReadOnly runs fixed read-only git plumbing. Arguments are constants,
// never model input, so there is no command-injection surface.
func runGitReadOnly(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
