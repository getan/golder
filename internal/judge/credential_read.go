package judge

// Credential-read detection for the static floor. Reading credential material
// looks harmless in isolation — `cat ~/.zshrc` is just a read — but the
// content can be exfiltrated by a later command, and shell rc files routinely
// carry exported API keys. Both checks are deterministic (no model involved)
// and match only a small, stable set of well-known locations. They exist so
// "read the keys" cannot pass as an allow merely because it looks passive;
// everything outside this narrow set is the reviewer's call.

import (
	"os"
	"path"
	"strings"
)

// credentialDirSegments are directory names whose entire subtree is credential
// material. Matched as a whole path segment, so a project file named
// `notes.ssh.md` is unaffected.
var credentialDirSegments = []string{".ssh", ".gnupg", ".aws"}

// credentialFileNames are file names (in any directory) that hold credentials
// or leak them: shell startup files and history routinely contain exported
// API keys and inline secrets.
var credentialFileNames = []string{
	".netrc",
	".git-credentials",
	".zshrc", ".zshenv", ".zprofile", ".zlogin",
	".bashrc", ".bash_profile", ".profile",
	".zsh_history", ".bash_history",
}

// credentialPrefixes are home-relative directory prefixes whose subtree is
// credential material (browser profiles and the macOS keychain). Kept
// home-relative so a same-named directory inside a project is unaffected.
var credentialPrefixes = []string{
	"Library/Keychains/",
	"Library/Application Support/Google/Chrome/",
	"Library/Application Support/Firefox/",
}

// credentialPathRead reports whether p names live credential material. Only
// paths resolving into the user's home are graded: a relative path is resolved
// against the workspace root, and an absolute path outside home is project or
// system content (a test fixture under testdata/.ssh, say), not the user's
// credentials. The caller passes the raw argument, so "~" is still literal
// here. Everything this function returns false for is the reviewer's call.
func credentialPathRead(p string) (string, bool) {
	raw := strings.TrimSpace(p)
	if !strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "~") {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	home = path.Clean(home)
	if strings.HasPrefix(raw, "~") {
		// "~" and "~/…" are the current user's home; other users' "~name" forms
		// are left to the reviewer.
		rest := strings.TrimPrefix(raw, "~")
		if rest != "" && !strings.HasPrefix(rest, "/") {
			return "", false
		}
		raw = home + rest
	}
	cleaned := path.Clean(raw)
	if cleaned != home && !strings.HasPrefix(cleaned, home+"/") {
		return "", false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(cleaned, home), "/")
	if rel == "" {
		return "", false
	}
	// Only the first component under home counts: $HOME/.ssh/… and $HOME/.zshrc
	// are the real credential locations, while a deeper $HOME/work/proj/.ssh
	// is project content (a fixture), so it stays readable. There is no way to
	// express that with a name-anywhere match without blocking fixtures too.
	segs := strings.Split(rel, "/")
	first := segs[0]
	for _, dir := range credentialDirSegments {
		if first == dir {
			return "credential material (home " + dir + "/)", true
		}
	}
	for _, name := range credentialFileNames {
		if first == name {
			return "credential material (home " + name + ")", true
		}
	}
	// Home-relative prefixes (browser profiles, keychain).
	for _, prefix := range credentialPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return "credential material (" + strings.TrimSuffix(prefix, "/") + ")", true
		}
	}
	return "", false
}

// CredentialPathReason is the exported form of credentialPathRead for callers
// outside the judge package (the file tools) that have already resolved a path
// and want to refuse it with the same wording the static floor uses.
func CredentialPathReason(p string) (string, bool) { return credentialPathRead(p) }

// credentialRead reports whether a shell command reads credential material.
// It scans the command's tokens for absolute or ~-anchored path spellings,
// which catches the common shapes (cat ~/.zshrc, grep KEY ~/.aws/credentials)
// without interpreting shell semantics — variables, globs, and pipelines are
// the reviewer's job. Relative tokens are skipped for the same reason as in
// credentialPathRead: they resolve against the workspace.
func credentialRead(cmd string) (string, bool) {
	for _, raw := range strings.Fields(cmd) {
		tok := strings.Trim(raw, `"'`+"`;|&()<>,=")
		if tok == "" {
			continue
		}
		if !strings.HasPrefix(tok, "/") && !strings.HasPrefix(tok, "~") {
			continue
		}
		if reason, bad := credentialPathRead(tok); bad {
			return "command reads " + reason, true
		}
	}
	return "", false
}
