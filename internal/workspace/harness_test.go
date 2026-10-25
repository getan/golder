package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/getan/golder/internal/hostcompat"
)

// ownedHarness is what these tests read back out of the file the workspace wrote.
type ownedHarness struct {
	ID     string `toml:"id"`
	Resume struct {
		Argv   []string `toml:"argv"`
		Source string   `toml:"source"`
	} `toml:"resume"`
}

// writeOwnedHarness writes this workspace's manifest for one executable and
// returns the file it wrote.
func writeOwnedHarness(t *testing.T, executable string) (*ownedHarness, string) {
	t.Helper()
	root := t.TempDir()
	b := &Backend{Executable: executable}
	dir, err := b.writeHarness(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "golder.toml")
	var got ownedHarness
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if got.ID != "golder" {
		t.Fatalf("harness id = %q, want golder", got.ID)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("harness is not a private file (mode %v, err %v)", st.Mode().Perm(), err)
	}
	return &got, path
}

// A path tuios can type unquoted is installed as itself, so a resume runs the
// same golder that owns the workspace rather than whichever golder a shell finds
// on PATH.
func TestOwnedHarnessResumesThroughThisExecutable(t *testing.T) {
	const executable = "/opt/golder/bin/golder-1.5.0"
	got, _ := writeOwnedHarness(t, executable)
	want := []string{executable, "--resume", "{session_id}"}
	if !reflect.DeepEqual(got.Resume.Argv, want) {
		t.Fatalf("resume argv = %q, want %q", got.Resume.Argv, want)
	}
	if !strings.Contains(got.Resume.Source, "same golder executable") {
		t.Fatalf("resume source = %q, want it to say which executable resumes", got.Resume.Source)
	}
}

// The install path is the daemon's to refuse, and it refuses the file rather
// than the block: one token it cannot type would cost the detection, the screen
// rules and the answers that let tuios' Inbox answer golder's prompts. So a
// program token it would refuse is not written at all — an empty [resume] is a
// harness that cannot be resumed, which is the truth, and every other block is
// unchanged.
func TestOwnedHarnessKeepsEveryOtherBlockWhenThePathCannotBeTyped(t *testing.T) {
	for _, executable := range []string{
		"/opt/My Golder/bin/golder",
		`/opt/golder-"beta"/golder`,
		"/Users/日本語/bin/golder",
		"/Users/o'brien/bin/golder",
		"/tmp/$HOME/golder",
		"/tmp/golder;rm -rf /",
	} {
		t.Run(executable, func(t *testing.T) {
			got, path := writeOwnedHarness(t, executable)
			if len(got.Resume.Argv) != 0 {
				t.Fatalf("resume argv = %q, want an empty block for a path tuios cannot type", got.Resume.Argv)
			}
			// The reason is in the manifest, where an operator reading the file
			// (or `tuios doctor agents`) finds it rather than guessing.
			named := strconv.Quote(filepath.Clean(executable))
			for _, want := range []string{"no resume command", named, "unquoted"} {
				if !strings.Contains(got.Resume.Source, want) {
					t.Fatalf("resume source = %q, want it to mention %q", got.Resume.Source, want)
				}
			}
			// Everything the daemon needs to recognise golder and read its
			// prompts is still there, block for block.
			shipped, written := manifestBlocks(t, hostcompat.ManifestSource()), manifestBlocks(t, readFile(t, path))
			delete(shipped, "resume")
			delete(written, "resume")
			if !reflect.DeepEqual(written, shipped) {
				t.Fatalf("the written manifest lost or changed a block:\ngot  %v\nwant %v", keys(written), keys(shipped))
			}
		})
	}
}

// Safe executable paths are kept absolute and are never a bare "golder": the
// manifest must not depend on PATH, and it must not be quoted either.
func TestResumeBlockKeepsTheExecutableAsItsOwnToken(t *testing.T) {
	for _, executable := range []string{
		"/opt/golder/bin/golder",
		"/Users/a_b/AppData:local/golder-1.5.0+beta",
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-golder-0.9/bin/golder",
	} {
		block := resumeBlock(executable)
		argv, _ := block["argv"].([]string)
		if want := []string{executable, "--resume", "{session_id}"}; !reflect.DeepEqual(argv, want) {
			t.Fatalf("resumeBlock(%q) argv = %q, want %q", executable, argv, want)
		}
	}
}

// The character set below is the daemon's, restated: letters, digits and
// _ . / : = + -, because those are the characters every shell tuios runs reads
// as themselves with no quoting. Pinning it byte by byte is what keeps this
// workspace's pre-check from drifting away from the rule it is predicting; the
// real daemon then proves the prediction end to end in harness_daemon_test.go.
func TestResumeTokenSetIsTheDaemons(t *testing.T) {
	for b := 0; b < 256; b++ {
		c := byte(b)
		want := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("_./:=+-", c) >= 0
		if got := resumeSafeToken(string([]byte{c})); got != want {
			t.Fatalf("resumeSafeToken(%q) = %v, want %v", c, got, want)
		}
	}
	if resumeSafeToken("") {
		t.Fatal("an empty token is not a token")
	}
	for _, unsafe := range []string{"a b", "a\tb", "a\nb", `a"b`, "a'b", "a`b", "a$b", "a~b", "a*b", "a?b",
		"a;b", "a&b", "a|b", "a(b", "a)b", "a<b", "a>b", "a[b", "a]b", "a{b", "a}b", "a#b", "a@b", "a,b",
		"a\\b", "a!b", "a%b", "a^b", "aéb", "日本語"} {
		if resumeSafeToken(unsafe) {
			t.Fatalf("resumeSafeToken(%q) = true, want false", unsafe)
		}
	}
}

// The rest of the daemon's checks are the same three: a block names the session,
// the program token holds no placeholder, and the command is not a paragraph.
func TestResumeArgvAcceptedMatchesTheDaemonsChecks(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"this workspace's own", []string{"/opt/golder/bin/golder", "--resume", "{session_id}"}, true},
		{"placeholder shares a token", []string{"/g", "--resume={session_id}"}, true},
		{"empty", nil, false},
		{"names no session", []string{"/g", "--resume"}, false},
		{"placeholder as the program", []string{"{session_id}", "--resume"}, false},
		{"a token a shell would split", []string{"/My Golder/g", "--resume", "{session_id}"}, false},
		{"too many tokens", append([]string{"/g"}, append(make([]string, maxResumeTokens), "{session_id}")...), false},
	}
	for _, tc := range cases {
		if got := resumeArgvAccepted(tc.argv); got != tc.want {
			t.Fatalf("%s: resumeArgvAccepted(%q) = %v, want %v", tc.name, tc.argv, got, tc.want)
		}
	}
}

// The manifest this workspace writes is never installed into the machine's own
// tuios harness directory: the embedded daemon reads its own, and an external
// host-compat install stays opt-in.
func TestOwnedHarnessNeverTouchesTheExternalHostDirectory(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "foreign")
	t.Setenv("TUIOS_HARNESS_DIR", foreign)
	b := &Backend{Executable: filepath.Join(root, "golder")}
	if _, err := b.writeHarness(filepath.Join(root, "own")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(foreign); !os.IsNotExist(err) {
		t.Fatal("workspace touched the external host's harness directory")
	}
	if _, err := b.writeHarness(filepath.Join(root, "own")); err != nil {
		t.Fatalf("restarting could not refresh its owned manifest: %v", err)
	}
}

// manifestBlocks decodes a manifest as blocks rather than as typed fields, so a
// comparison sees every block the file declares, including the ones nothing here
// reads.
func manifestBlocks(t *testing.T, source string) map[string]any {
	t.Helper()
	var doc map[string]any
	if _, err := toml.Decode(source, &doc); err != nil {
		t.Fatalf("decode a manifest: %v", err)
	}
	return doc
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func keys(doc map[string]any) []string {
	out := make([]string, 0, len(doc))
	for name := range doc {
		out = append(out, name)
	}
	return out
}
