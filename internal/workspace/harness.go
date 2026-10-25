package workspace

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/getan/golder/internal/cli/workspacecmd"
	"github.com/getan/golder/internal/hostcompat"
	"github.com/getan/golder/internal/privatepath"
)

// The resume block this workspace installs is checked against the daemon's own
// rule before it is written, because the daemon validates a manifest as one
// file: a [resume] block it refuses does not cost the resume command alone, it
// costs the detection, the screen rules and every answer binding in the file,
// which is what makes the Inbox able to answer golder's prompts at all.
//
// The daemon's rule is in the pinned tuios (internal/harness/resume.go), and it
// is not importable from here: that package is internal to another module. So the
// constants and checks below restate it. They restate a published
// manifest format rather than an implementation detail — the format is what a
// manifest author has to satisfy — and harness_test.go pins the character set byte
// by byte, with a real daemon proving the verdict end to end in
// harness_daemon_test.go. A change to the daemon's rule that this misses would
// therefore be caught by that test rather than by a user losing their screen
// rules.
const (
	// resumePlaceholder is where a resume command's session id goes.
	resumePlaceholder = "{session_id}"
	// resumeTokenPunctuation is every punctuation byte a resume token may hold.
	resumeTokenPunctuation = "_./:=+-"
	// maxResumeTokens is the daemon's bound on a resume command.
	maxResumeTokens = 16
	// resumeFlag is the flag a harness is reopened with. The program token in
	// front of it is this workspace's own executable, so a resume never depends
	// on a "golder" being found on PATH.
	resumeFlag = "--resume"
)

// resumeTokenByte reports whether b may appear in a resume token as typed: a
// letter, a digit, or one of _ . / : = + -, which every shell tuios runs reads as
// itself with no quoting.
func resumeTokenByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte(resumeTokenPunctuation, b) >= 0
}

// resumeSafeToken reports whether s is one token the daemon accepts: the
// characters a shell reads as themselves, unquoted, in every shell tuios runs.
func resumeSafeToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !resumeTokenByte(s[i]) {
			return false
		}
	}
	return true
}

// resumeArgvAccepted reports whether the daemon will keep argv, checked the way
// its normalize does: the program token holds no placeholder, every other token
// holds only characters a shell reads as themselves once the placeholder is
// removed, and one token names the session.
func resumeArgvAccepted(argv []string) bool {
	if len(argv) == 0 || len(argv) > maxResumeTokens {
		return false
	}
	if strings.Contains(argv[0], resumePlaceholder) {
		return false
	}
	names := false
	for _, tok := range argv {
		if strings.Contains(tok, resumePlaceholder) {
			names = true
		}
		rest := strings.ReplaceAll(tok, resumePlaceholder, "")
		if tok != "" && rest == "" {
			continue
		}
		if !resumeSafeToken(rest) {
			return false
		}
	}
	return names
}

// resumeBlock is the [resume] block for the harness this workspace installs:
// this executable when its path is a token the daemon accepts, and an empty argv
// when it is not.
//
// The empty block is the whole of the degradation. The daemon treats a [resume]
// with no argv as a harness that cannot be resumed, which is the truth here, and
// keeps the rest of the file; a rejected token would take the file with it. It is
// not a fall back to a bare "golder" on PATH, which would resume through whichever
// golder the pane's shell happens to find, and it is not quoting: a quoted token
// is still refused by the daemon, which does not quote and never will.
func resumeBlock(executable string) map[string]any {
	argv := []string{executable, resumeFlag, resumePlaceholder}
	if resumeArgvAccepted(argv) {
		return map[string]any{
			"argv":   argv,
			"source": "resume through the same golder executable that owns this workspace",
		}
	}
	return map[string]any{
		"argv":   []string{},
		"source": fmt.Sprintf("no resume command here: tuios types a resume command into the pane's shell unquoted, and this workspace's golder executable (%q) holds a character it will not type, so this block names no argv. Detection, the screen rules and their answers are unaffected", executable),
	}
}

// writeHarness installs only into this daemon's owned state. The external
// host-compat installer remains opt-in and never runs on this path.
func (b *Backend) writeHarness(stateDir string) (string, error) {
	command, err := b.initialCommand(workspacecmd.Options{})
	if err != nil {
		return "", err
	}
	var manifest map[string]any
	if _, err := toml.Decode(hostcompat.ManifestSource(), &manifest); err != nil {
		return "", fmt.Errorf("workspace: decode bundled golder harness: %w", err)
	}
	manifest["resume"] = resumeBlock(command[0])
	var body bytes.Buffer
	if err := toml.NewEncoder(&body).Encode(manifest); err != nil {
		return "", fmt.Errorf("workspace: encode own harness: %w", err)
	}
	dir := filepath.Join(stateDir, "harnesses")
	if err := privatepath.EnsureDir(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "golder.toml")
	if _, err := os.Lstat(path); err == nil {
		if err := privatepath.CheckFile(path, 0o600); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".golder-harness-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body.Bytes()); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return dir, nil
}
