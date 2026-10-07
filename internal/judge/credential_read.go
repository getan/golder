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

// contentReaders are commands whose arguments are file content: reading,
// copying, archiving, or hashing. Only for these does a quoted path argument
// count too (`cat "~/.zshrc"`); every other command is graded on its bare
// (unquoted) path tokens, so a quoted piece of prose — an echo label, a
// commit message, a search pattern — falls through to the reviewer. The list
// is the common coreutils/editor core on purpose; anything else is the
// reviewer's call, and the sandbox still denies the path in every mode that
// runs commands isolated.
var contentReaders = map[string]bool{
	"cat": true, "tac": true, "head": true, "tail": true, "less": true,
	"more": true, "most": true, "bat": true, "nl": true, "od": true,
	"xxd": true, "hexdump": true, "strings": true, "base64": true,
	"base32": true, "grep": true, "egrep": true, "fgrep": true, "rg": true,
	"ag": true, "ack": true, "sed": true, "awk": true, "gawk": true,
	"sort": true, "uniq": true, "cut": true, "wc": true, "file": true,
	"stat": true, "diff": true, "cmp": true, "shasum": true,
	"sha1sum": true, "sha256sum": true, "md5": true, "openssl": true,
	"cp": true, "mv": true, "install": true, "rsync": true, "scp": true,
	"sftp": true, "tar": true, "zip": true, "unzip": true, "gzip": true,
	"gunzip": true, "source": true, ".": true, "open": true, "code": true,
	"vim": true, "vi": true, "nano": true, "emacs": true, "gpg": true,
}

// patternCommands take a pattern or script as their first non-flag argument,
// so that position is not a path: `grep -rn "~/.zshrc" docs/` searches the
// docs, it does not read the shell rc.
var patternCommands = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"ack": true, "sed": true, "awk": true, "gawk": true,
}

// credentialRead reports whether a shell command reads credential material.
// Each shell segment is read at command position (transparent prefixes and
// shell one-liners are unwrapped), and only path-shaped words are graded:
// absolute or ~-anchored spellings, matched by credentialPathRead. Quoted
// words count only for file-reading commands, so prose that happens to
// mention ~/.zshrc — a commit message, an echo label, a search pattern — is
// the reviewer's call rather than a hard deny. Variables, globs, and
// pipelines stay the reviewer's job; relative paths resolve against the
// workspace and are skipped for the same reason as in credentialPathRead.
// The macOS keychain is covered on both axes: a keychain file path is graded
// like any other credential path, and the security CLI's secret-printing
// forms (find-*-password -w/-g, dump-keychain, export) are denied by
// keychainSecret.
func credentialRead(cmd string) (string, bool) {
	for _, seg := range splitShellSegments(cmd) {
		words := shellWords(seg)
		i := 0
		for i < len(words) && isTransparentPrefix(words[i].text) {
			i++
		}
		if i >= len(words) {
			continue
		}
		base := path.Base(words[i].text)
		rest := words[i+1:]
		if inner, ok := shellDashC(base, plainWords(rest)); ok {
			if reason, bad := credentialRead(inner); bad {
				return reason, true
			}
			continue
		}
		if reason, bad := keychainSecret(base, plainWords(rest)); bad {
			return reason, true
		}
		reader := contentReaders[base]
		patternPending := patternCommands[base]
		for _, w := range rest {
			if strings.HasPrefix(w.text, "-") {
				continue
			}
			if patternPending {
				patternPending = false
				continue
			}
			// A quoted word is only a path when the command reads files; for
			// everything else it is text (a message, a label, a pattern).
			if w.quoted && !reader {
				continue
			}
			tok := strings.Trim(w.text, "`;|&()<>,=")
			if tok == "" || (!strings.HasPrefix(tok, "/") && !strings.HasPrefix(tok, "~")) {
				continue
			}
			if reason, bad := credentialPathRead(tok); bad {
				return "command reads " + reason, true
			}
		}
	}
	return "", false
}

// keychainSecret reports whether a command extracts a secret from the macOS
// keychain through the `security` CLI: find-generic-password /
// find-internet-password asked to print the password (-w, or -g which prints
// it to stderr), dump-keychain, or export. Those are the shapes that hand the
// secret to the calling process, so they are denied exactly like reading
// ~/.zshrc. Attribute-only lookups (list-keychains, find-certificate -p,
// find-*-password without -w/-g) and everything else the CLI offers stay with
// the reviewer.
func keychainSecret(base string, args []string) (string, bool) {
	if base != "security" {
		return "", false
	}
	sub := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			sub = a
			break
		}
	}
	switch sub {
	case "find-generic-password", "find-internet-password":
		for _, a := range args {
			// A print flag is a short cluster made only of w (print the
			// password) and g (print it to stderr), so a value like
			// "-webuser" after -a is not mistaken for one.
			if len(a) >= 2 && a[0] == '-' && a[1] != '-' && strings.Trim(a[1:], "wg") == "" {
				return "command extracts a password from the macOS keychain (security " + sub + ")", true
			}
		}
	case "dump-keychain", "export":
		return "command dumps the macOS keychain (security " + sub + ")", true
	}
	return "", false
}

// plainWords flattens shell words back to strings for callers that need only
// the text (shell unwrapping).
func plainWords(words []shellWord) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		out = append(out, w.text)
	}
	return out
}
