package hostcompat

import (
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/provider"
)

// TestIdentityEnvIsAnExplicitList pins the whole allow list, because it is a
// security boundary rather than a convenience: everything it names is put back
// into a spawned child's environment after the credential scrub. A new entry
// has to be argued for here, in front of a test that says what the list is for.
func TestIdentityEnvIsAnExplicitList(t *testing.T) {
	want := []string{
		"GOLDER_WORKSPACE",
		"HERDR_BIN_PATH",
		"HERDR_ENV",
		"HERDR_PANE_ID",
		"HERDR_SOCKET_PATH",
		"TUIOS_ENV",
		"TUIOS_HOST",
		"TUIOS_PANE_ID",
		"TUIOS_PANE_TOKEN",
		"TUIOS_PANE_TTY",
		"TUIOS_SESSION",
		"TUIOS_SOCKET",
	}
	got := append([]string(nil), identityVars...)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("identityVars = %v, want %v", got, want)
	}
}

// TestIdentityEnvRecoversWhatTheScrubRemoves is why the allow list exists: only
// the workspace marker and the pane's own token are caught by the credential
// scrub (one for its prefix, one for the word in its name), and those two are
// exactly what a spawned golder needs to know which pane it is. Everything else
// on the list the scrub leaves alone, so it is inherited on its own.
func TestIdentityEnvRecoversWhatTheScrubRemoves(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin:/bin",
		"GOLDER_WORKSPACE=1",
		"TUIOS_ENV=1",
		"TUIOS_SOCKET=/tmp/workspace.sock",
		"TUIOS_SESSION=work",
		"TUIOS_PANE_ID=pane-7",
		"TUIOS_PANE_TOKEN=proof-of-pane-7",
		"DEEPSEEK_API_KEY=sk-secret",
	}
	scrubbed := provider.ScrubEnv(parent, nil)
	for _, keep := range []string{"TUIOS_ENV", "TUIOS_SOCKET", "TUIOS_SESSION", "TUIOS_PANE_ID"} {
		if !hasEnv(scrubbed, keep) {
			t.Errorf("%s is scrubbed; the identity list must carry it back", keep)
		}
	}
	for _, gone := range []string{"GOLDER_WORKSPACE", "TUIOS_PANE_TOKEN", "DEEPSEEK_API_KEY"} {
		if hasEnv(scrubbed, gone) {
			t.Errorf("%s survived the scrub", gone)
		}
	}
	// With the identity allow list the pane is whole again, and the credential
	// is still gone. A caller re-injecting it drops the names the scrub keeps
	// anyway (runtime.subAgentEnv does), so no variable is set twice.
	allow := IdentityEnv(parent)
	for name := range allow {
		if !provider.ScrubsCredentialVar(name) {
			delete(allow, name)
		}
	}
	derived := provider.ScrubEnv(parent, allow)
	for _, want := range []string{"GOLDER_WORKSPACE", "TUIOS_PANE_TOKEN", "TUIOS_PANE_ID"} {
		if !hasEnv(derived, want) {
			t.Errorf("%s is missing from the derived environment: %v", want, derived)
		}
	}
	for _, gone := range []string{"DEEPSEEK_API_KEY", "GITHUB_TOKEN", "ANTHROPIC_API_KEY"} {
		if hasEnv(derived, gone) {
			t.Errorf("%s rode along: %v", gone, derived)
		}
	}
	if dup := duplicateNames(derived); len(dup) > 0 {
		t.Errorf("derived environment repeats %v; a name appears once", dup)
	}
}

// hasEnv reports whether entries contain NAME=anything.
func hasEnv(entries []string, name string) bool {
	for _, entry := range entries {
		if strings.HasPrefix(entry, name+"=") {
			return true
		}
	}
	return false
}

// duplicateNames lists names that appear more than once in an environment.
func duplicateNames(entries []string) []string {
	seen := map[string]bool{}
	var dup []string
	for _, entry := range entries {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if seen[name] {
			dup = append(dup, name)
		}
		seen[name] = true
	}
	return dup
}

// TestIdentityEnvKeepsOnlyPaneIdentity: the allow list carries the values of
// those names and nothing else, so a credential-shaped parent variable cannot
// ride along just because it shares a prefix or a suffix with something.
func TestIdentityEnvKeepsOnlyPaneIdentity(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/person",
		"GOLDER_WORKSPACE=1",
		"TUIOS_ENV=1",
		"TUIOS_SOCKET=/run/user/1000/golder/workspace.sock",
		"TUIOS_SESSION=work",
		"TUIOS_PANE_ID=pane-7",
		"TUIOS_PANE_TOKEN=proof-of-pane-7",
		"TUIOS_HOOK_TOKEN=hook-secret",
		// The shapes that must never be inherited.
		"GOLDER_HOME=/home/person/.golder",
		"GOLDER_HOST_REPORT=tuios",
		"GOLDER_API_KEY=secret",
		"ANTHROPIC_API_KEY=secret",
		"GITHUB_TOKEN=secret",
		"AWS_SECRET_ACCESS_KEY=secret",
		"TUIOS_API_TOKEN=secret",
		"malformed-entry",
	}
	allow := IdentityEnv(parent)
	want := map[string]string{
		"GOLDER_WORKSPACE": "1",
		"TUIOS_ENV":        "1",
		"TUIOS_SOCKET":     "/run/user/1000/golder/workspace.sock",
		"TUIOS_SESSION":    "work",
		"TUIOS_PANE_ID":    "pane-7",
		"TUIOS_PANE_TOKEN": "proof-of-pane-7",
	}
	for name, value := range want {
		if got := allow[name]; got != value {
			t.Errorf("allow[%s] = %q, want %q", name, got, value)
		}
	}
	if len(allow) != len(want) {
		t.Errorf("allow = %v, want exactly the pane identity", allow)
	}
}

// TestIdentityEnvEdgeValues: an empty value is not an identity (it would send a
// child an empty socket path or an empty token), and a duplicated name keeps
// the first value, which is how the operating system resolves one.
func TestIdentityEnvEdgeValues(t *testing.T) {
	allow := IdentityEnv([]string{
		"TUIOS_PANE_ID=",
		"TUIOS_SESSION=work",
		"TUIOS_SESSION=other",
		"TUIOS_SOCKET",
	})
	if _, ok := allow["TUIOS_PANE_ID"]; ok {
		t.Error("an empty identity value was kept")
	}
	if get := allow["TUIOS_SESSION"]; get != "work" {
		t.Errorf("TUIOS_SESSION = %q, want the first value", get)
	}
	if _, ok := allow["TUIOS_SOCKET"]; ok {
		t.Error("an entry with no value was kept")
	}
	if allow := IdentityEnv(nil); len(allow) != 0 {
		t.Errorf("IdentityEnv(nil) = %v, want empty", allow)
	}
}
