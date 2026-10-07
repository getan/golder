package judge

// Tests for the credential-read static floor: it must catch the obvious
// "read the keys" shapes deterministically, and must NOT fire on project
// content that merely shares a name with a credential path (a test fixture
// under testdata/.ssh, a project file called .zshrc, and so on).

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCredentialPathReadMatchesRealLocations(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cases := []string{
		"~/.zshrc",
		"~/.ssh/config",
		"~/.bashrc",
		home + "/.zshrc",
		home + "/.ssh/id_rsa",
		home + "/.gnupg/secring.gpg",
		home + "/.aws/credentials",
		home + "/.netrc",
		home + "/.git-credentials",
		home + "/.zsh_history",
		home + "/Library/Keychains/login.keychain-db",
		"~/Library/Application Support/Google/Chrome/Default/Cookies",
	}
	for _, p := range cases {
		if reason, bad := credentialPathRead(p); !bad {
			t.Errorf("credentialPathRead(%q) = (%q, false), want a denial", p, reason)
		}
	}
}

func TestCredentialPathReadIgnoresProjectContent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cases := []string{
		"testdata/.ssh/config",       // fixture inside the workspace
		"./fixtures/.aws/creds.json", // relative → resolves to the project
		"docs/.zshrc.example",        // documentation sample
		"internal/judge/credential_read.go",
		"testdata/Library/Keychains/x", // relative fixture
		"~someoneelse/.ssh/id_rsa",     // another user's home: reviewer's call
		"/tmp/project/.netrc",          // absolute but not under a home dir
		// A fixture nested under home but not a first-level credential entry:
		// $HOME/.ssh is credential material, $HOME/work/proj/.ssh is a fixture.
		home + "/work/proj/testdata/.ssh/config",
		home + "/work/proj/.netrc",
	}
	for _, p := range cases {
		if reason, bad := credentialPathRead(p); bad {
			t.Errorf("credentialPathRead(%q) = (%q, true), want no denial", p, reason)
		}
	}
}

func TestCredentialReadCommand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	denied := []string{
		"cat ~/.zshrc",
		"cat " + home + "/.zshrc",
		"grep -i api_key ~/.aws/credentials",
		"tar czf - ~/.ssh",
		"head -5 ~/.bash_history",
		// Quoting a path does not make the read disappear for a file-reading
		// command, and a shell one-liner is unwrapped before grading.
		`cat "~/.zshrc"`,
		`sh -c "cat ~/.zshrc"`,
		`bash -lc "grep KEY ~/.ssh/config"`,
		`cp ~/.ssh/id_rsa /tmp/x`,
		`source ~/.zshrc`,
		`grep "api_key" "~/.aws/credentials"`,
		`sed -n '1,5p' ~/.zshrc`,
		`openssl rsa -in ~/.ssh/id_rsa -noout`,
		`cd ~/.ssh && cat id_rsa`,
	}
	for _, cmd := range denied {
		if reason, bad := credentialRead(cmd); !bad {
			t.Errorf("credentialRead(%q) = (%q, false), want a denial", cmd, reason)
		}
	}
	allowed := []string{
		"cat README.md",
		"cat testdata/.ssh/config",
		"git status",
		"go test ./...",
		"cat ~/work/project/notes.txt",
		"grep -rn TODO .",
		// Prose that mentions a credential path is not a read: an echo label,
		// a commit message, a search pattern, a quoted append target.
		`echo "~/.ssh/" >> .gitignore`,
		`git commit -m "docs: 说明 ~/.zshrc 配置"`,
		`grep -rn "~/.zshrc" docs/`,
		`rg -n "~/.ssh" internal/`,
		`printf '%s\n' "~/.netrc" > /tmp/ignore.txt`,
		`echo "~/.zshrc"`,
		`git commit -m "fix: stop reading ~/.netrc"`,
	}
	for _, cmd := range allowed {
		if reason, bad := credentialRead(cmd); bad {
			t.Errorf("credentialRead(%q) = (%q, true), want no denial", cmd, reason)
		}
	}
}

// TestStaticDenyCredentialReads drives the whole static floor for the read and
// grep tools plus bash, so a wiring mistake (missing case) fails here.
func TestStaticDenyCredentialReads(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cases := []struct {
		tool string
		args string
		want bool
	}{
		{"read", `{"path":"~/.zshrc"}`, true},
		{"read", `{"path":"` + home + `/.ssh/id_rsa"}`, true},
		{"read", `{"path":"README.md"}`, false},
		{"read", `{"path":"testdata/.zshrc"}`, false},
		{"grep", `{"pattern":"KEY","path":"~/.aws"}`, true},
		{"grep", `{"pattern":"TODO","path":"internal"}`, false},
		{"bash", `{"command":"cat ~/.zshrc"}`, true},
		{"bash", `{"command":"echo hi"}`, false},
	}
	for _, c := range cases {
		_, bad := staticDeny(c.tool, json.RawMessage(c.args))
		if bad != c.want {
			t.Errorf("staticDeny(%s, %s) denied=%v, want %v", c.tool, c.args, bad, c.want)
		}
	}
}
