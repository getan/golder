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

// Supported reports that this platform has a sandbox implementation.
func Supported() bool { return true }

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
	return r.SandboxCall([]string{shell, flag, command}, dir, CallPolicy{})
}

// SandboxCall confines an arbitrary one-shot tool process with per-call grants.
func (r *SeatbeltRunner) SandboxCall(argv []string, _ string, policy CallPolicy) ([]string, func(), error) {
	profile, err := r.writeCallProfile(policy)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.Remove(profile) }
	return append([]string{"sandbox-exec", "-f", profile}, argv...), cleanup, nil
}

func (r *SeatbeltRunner) tmpDir() string {
	if r.TmpDir != "" {
		return r.TmpDir
	}
	return os.TempDir()
}

func quoteAll(paths []string) string {
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, fmt.Sprintf("(subpath %s)", quote(p)))
	}
	return strings.Join(quoted, " ")
}

// ancestorChain returns the parent directories of p, from "/" down to p's
// direct parent, canonical. The profile grants them metadata-only reads: a
// process cannot resolve a path (or even learn its own cwd) if stat on the
// ancestors is denied, and metadata carries no file content — so this keeps
// path traversal working without exposing anything. "/" is included because
// looking up any absolute path starts there.
func ancestorChain(p string) []string {
	if p == "" {
		return nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil
	}
	var chain []string
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if dir == "." || dir == "" {
			break
		}
		chain = append(chain, dir)
		if dir == "/" {
			break
		}
	}
	// Root-most first, each in both symlink spellings.
	var out []string
	for i := len(chain) - 1; i >= 0; i-- {
		out = append(out, canonicals(chain[i])...)
	}
	return out
}

// writeProfile renders the seatbelt profile with absolute paths and stores it
// under the temp dir. Both directions are whitelists: reads are allowed only
// for the project, the system runtime and ReadableRoots (so the rest of $HOME,
// including shell rc files and credential stores, is unreadable by
// construction); writes are confined to the project and temp dirs. Network is
// denied unless the gate granted it for this one call (CallPolicy.Network), so
// a sandboxed command cannot exfiltrate what it read without a decision.
func (r *SeatbeltRunner) writeProfile() (string, error) {
	return r.writeCallProfile(CallPolicy{})
}

func (r *SeatbeltRunner) writeCallProfile(policy CallPolicy) (string, error) {
	project := r.ProjectDir
	if project == "" {
		var err error
		project, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("seatbelt: resolve project dir: %w", err)
		}
	}
	// Read whitelist: the project plus the system runtime, extra roots, and the
	// session's writable grants. Writable roots are readable too by
	// construction: writing a file the command may not read back would only
	// produce confusing half-failures. Every entry carries its canonical
	// spelling (see canonicals): the profile matches by vnode path, and macOS
	// temp trees live under the /var → /private/var symlink.
	//
	// "/" itself is added literally: subpath rules cover a directory and its
	// descendants, so without it the root directory is unreadable and every
	// absolute-path lookup fails — the process aborts before main (dyld cannot
	// resolve its own image). The root listing carries no user data.
	// The sandbox temp dir is readable and writable: toolchains spill there and
	// read their own spill back, so a write-only entry would fail confusingly.
	readRoots := append([]string{project, r.tmpDir()}, ReadableRoots()...)
	readRoots = append(readRoots, WritableRoots()...)
	readRoots = append(readRoots, policy.ReadDirs...)
	readRoots = append(readRoots, policy.WriteDirs...)
	// A one-call grant is exact: add it as a literal below, NOT to readRoots
	// (which are subpath rules covering an entire directory).
	readRule := fmt.Sprintf("(allow file-read* (literal \"/\") %s)", quoteAll(canonicalsAll(readRoots)))
	// Parent chains need metadata traversal so path resolution (and getcwd)
	// work: an allow on /a/b/c does not by itself permit stat on /a/b, and the
	// sandbox denies the whole lookup without it. This covers every read root,
	// including the git files under $HOME.
	metaRule := ""
	var metaDirs []string
	for _, root := range readRoots {
		metaDirs = append(metaDirs, ancestorChain(root)...)
	}
	for _, p := range append(append([]string{}, policy.ReadFiles...), policy.WriteFiles...) {
		metaDirs = append(metaDirs, ancestorChain(p)...)
	}
	if metaDirs = dedupePaths(metaDirs); len(metaDirs) > 0 {
		// literal, not subpath: only the ancestor directories themselves need
		// stat for path traversal, and a subpath rule would additionally expose
		// the existence of every name below them.
		quoted := make([]string, 0, len(metaDirs))
		for _, dir := range metaDirs {
			quoted = append(quoted, fmt.Sprintf("(literal %s)", quote(dir)))
		}
		metaRule = fmt.Sprintf("(allow file-read-metadata %s)\n", strings.Join(quoted, " "))
	}
	network := "(deny network*)"
	if policy.Network {
		network = "(allow network*)"
	}
	// Golder's own state: denied read AND write after the allow rules, so a
	// project rooted inside $GOLDER_HOME cannot reach the trust store or
	// credential file. The basename regex also covers a trust.json sitting
	// anywhere else in an otherwise writable tree.
	denyGolder := ""
	if protected := ProtectedGolderPaths(); len(protected) > 0 {
		denyGolder = fmt.Sprintf("(deny file-read* file-write* %s)\n", quoteAll(canonicalsAll(protected)))
	}
	var b strings.Builder
	b.WriteString("(version 1)\n")
	// Default deny first: every allow below is explicit, and the write/deny
	// rules come last so they override the broader allows (later rules win).
	b.WriteString("(deny default)\n")
	b.WriteString("(allow process-exec process-fork)\n")
	b.WriteString("(allow job-creation)\n")
	b.WriteString("(allow signal (target self))\n")
	b.WriteString("(allow mach-lookup)\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString(readRule + "\n")
	b.WriteString(metaRule)
	if len(policy.ReadFiles)+len(policy.WriteFiles) > 0 {
		paths := append(append([]string{}, policy.ReadFiles...), policy.WriteFiles...)
		fmt.Fprintf(&b, "(allow file-read* %s)\n", literalAll(canonicalsAll(paths)))
	}
	var writeRoots []string
	if !policy.ReadOnlyFilesystem {
		writeRoots = append(canonicals(project), canonicals(r.tmpDir())...)
		for _, root := range WritableRoots() {
			writeRoots = append(writeRoots, canonicals(root)...)
		}
		for _, root := range policy.WriteDirs {
			writeRoots = append(writeRoots, canonicals(root)...)
		}
	}
	fmt.Fprintf(&b, "(allow file-write* %s (literal \"/dev/null\") (literal \"/dev/tty\"))\n",
		quoteAll(dedupePaths(writeRoots)))
	if len(policy.WriteFiles) > 0 && !policy.ReadOnlyFilesystem {
		fmt.Fprintf(&b, "(allow file-write* %s)\n", literalAll(canonicalsAll(policy.WriteFiles)))
	}
	// A denied child must override a broader parent allow (project root or a
	// directory grant). Keep exact refusals literal; a subtree refusal blocks
	// everything below it. These rules come after all file allows.
	if len(policy.DenyReadDirs) > 0 {
		fmt.Fprintf(&b, "(deny file-read* %s)\n", quoteAll(canonicalsAll(policy.DenyReadDirs)))
	}
	if len(policy.DenyReadFiles) > 0 {
		fmt.Fprintf(&b, "(deny file-read* %s)\n", literalAll(canonicalsAll(policy.DenyReadFiles)))
	}
	if len(policy.DenyWriteDirs) > 0 {
		fmt.Fprintf(&b, "(deny file-write* %s)\n", quoteAll(canonicalsAll(policy.DenyWriteDirs)))
	}
	if len(policy.DenyWriteFiles) > 0 {
		fmt.Fprintf(&b, "(deny file-write* %s)\n", literalAll(canonicalsAll(policy.DenyWriteFiles)))
	}
	b.WriteString(denyGolder)
	b.WriteString("(deny file-write* (regex #\".*trust\\.json$\"))\n")
	b.WriteString(network + "\n")
	profile := b.String()
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

// literalAll grants only the named files, never an entire directory subtree.
func literalAll(paths []string) string {
	var b strings.Builder
	for _, p := range dedupePaths(paths) {
		fmt.Fprintf(&b, " (literal %s)", quote(p))
	}
	return b.String()
}

// quote renders an absolute path as a double-quoted profile string.
func quote(p string) string {
	return "\"" + strings.ReplaceAll(p, "\"", "\\\"") + "\""
}
