// Package toolchain resolves explicitly requested execution profiles. A Plan
// describes capabilities; it grants none. Hosts must authorize and bind it.
package toolchain

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

// Plan is locally resolved evidence, not a permission grant.
type Plan struct {
	Name       string   `json:"name"`
	Executable string   `json:"executable"`
	GoRoot     string   `json:"goroot"`
	Binding    string   `json:"binding"`
	ReadDirs   []string `json:"read_dirs"`
	WriteDirs  []string `json:"write_dirs"`
	ReadFiles  []string `json:"read_files,omitempty"`
}

type GoPaths struct{ GOROOT, GOMODCACHE, GOCACHE, GOENV string }

// Resolve queries only a real system-installed cmd/go, outside the project.
// No shell, module hooks or toolchain download runs during this probe.
func Resolve(ctx context.Context, cwd, name string) (Plan, error) {
	if name != "go" && name != "go-download" {
		return Plan{}, errors.New("unknown execution profile")
	}
	if !seatbelt.Supported() {
		return Plan{}, errors.New("execution profile requires an OS sandbox")
	}
	path, err := findInstalledGo(cwd, os.Getenv("PATH"))
	if err != nil {
		return Plan{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Plan{}, errors.New("cannot identify installed Go executable")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, 256<<20))
	_ = file.Close()
	if err != nil || n == 256<<20 {
		return Plan{}, errors.New("Go executable identity exceeds limits")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, path, "env", "-json", "GOROOT", "GOMODCACHE", "GOCACHE", "GOENV")
	cmd.Dir = os.TempDir()
	cmd.Env = probeEnvironment(os.Environ())
	// A Go test host may export its own downloaded GOROOT. The selected SDK
	// must query and execute against its own matching compiler distribution.
	cmd.Env = append(cmd.Env, "GOROOT="+filepath.Dir(filepath.Dir(path)))
	var output boundedOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return Plan{}, errors.New("Go path probe failed or timed out; no command was run")
	}
	var paths GoPaths
	if json.Unmarshal(output.data, &paths) != nil {
		return Plan{}, errors.New("Go path probe returned invalid data")
	}
	return BuildGo(name, path, hex.EncodeToString(h.Sum(nil)), paths)
}

// Profiles explicitly select a real installed Go, not an opaque user shim.
// Search PATH first, then standard installation locations (Finder's PATH is
// often shorter than a terminal's). No candidate is executed for discovery.
func findInstalledGo(cwd, search string) (string, error) {
	dirs := append(filepath.SplitList(search), "/opt/homebrew/bin", "/usr/local/go/bin", "/usr/local/bin", "/usr/bin")
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			continue
		}
		path := permissions.Canonical(filepath.Join(dir, "go"))
		if permissions.InsideResolved(cwd, path) || !installedGoPath(path) {
			continue
		}
		info, err := buildinfo.ReadFile(path)
		if err == nil && info.Path == "cmd/go" {
			return path, nil
		}
	}
	return "", errors.New("Go profile requires a real system-installed cmd/go; wrappers and workspace executables are not probed")
}

func installedGoPath(path string) bool {
	for _, root := range []string{"/usr", "/opt", "/Library/Developer"} {
		if permissions.InsideResolved(root, path) {
			return true
		}
	}
	return false
}

func probeEnvironment(env []string) []string {
	keys := []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTELEMETRY=off"}
	out := make([]string, 0, len(env)+len(keys))
	for _, pair := range env {
		key, _, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(key, "DYLD_") || strings.HasPrefix(key, "LD_") || key == "GOFLAGS" || key == "GOCACHEPROG" {
			continue
		}
		if !slices.ContainsFunc(keys, func(v string) bool { return strings.HasPrefix(v, key+"=") }) {
			out = append(out, pair)
		}
	}
	return append(out, keys...)
}

type boundedOutput struct{ data []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 32<<10 {
		return 0, errors.New("Go probe output exceeds limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

// BuildGo is the deterministic manifest compiler, independent of discovery.
func BuildGo(name, executable, identity string, paths GoPaths) (Plan, error) {
	if name != "go" && name != "go-download" {
		return Plan{}, errors.New("unknown execution profile")
	}
	p := Plan{Name: name, Executable: executable, GoRoot: permissions.Canonical(paths.GOROOT)}
	for _, raw := range []string{paths.GOROOT, paths.GOMODCACHE, paths.GOCACHE} {
		if err := safeDirectory(raw); err != nil {
			return Plan{}, err
		}
	}
	p.ReadDirs = []string{permissions.Canonical(paths.GOROOT), permissions.Canonical(paths.GOMODCACHE)}
	p.WriteDirs = []string{permissions.Canonical(paths.GOCACHE)}
	if overlaps(p.WriteDirs[0], p.ReadDirs[1]) || overlaps(p.WriteDirs[0], p.GoRoot) {
		return Plan{}, errors.New("build cache overlaps the read-only SDK or module cache")
	}
	if name == "go-download" {
		if overlaps(p.ReadDirs[1], p.GoRoot) {
			return Plan{}, errors.New("writable module cache overlaps the selected installed SDK")
		}
		p.WriteDirs = append(p.WriteDirs, permissions.Canonical(paths.GOMODCACHE))
	}
	for _, cache := range p.WriteDirs {
		if err := safeCache(cache); err != nil {
			return Plan{}, err
		}
	}
	if paths.GOENV != "" && paths.GOENV != "off" {
		if !filepath.IsAbs(paths.GOENV) {
			return Plan{}, errors.New("Go environment configuration path is not absolute")
		}
		if info, err := os.Stat(paths.GOENV); err == nil && info.Mode().IsRegular() {
			if err := safeDirectory(filepath.Dir(paths.GOENV)); err != nil {
				return Plan{}, err
			}
			p.ReadFiles = []string{permissions.Canonical(paths.GOENV)}
			file, err := os.Open(paths.GOENV)
			if err != nil {
				return Plan{}, errors.New("Go environment configuration cannot be read")
			}
			data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
			_ = file.Close()
			if err != nil || len(data) > 64<<10 {
				return Plan{}, errors.New("Go environment configuration cannot be bound safely")
			}
			sum := sha256.Sum256(data)
			identity += hex.EncodeToString(sum[:])
		}
	}
	data, _ := json.Marshal(struct {
		Plan               Plan
		Identity, Platform string
	}{p, identity, runtime.GOOS})
	sum := sha256.Sum256(data)
	p.Binding = "toolchain-v1:" + hex.EncodeToString(sum[:])
	return p, nil
}

func safeDirectory(raw string) error {
	if !filepath.IsAbs(raw) {
		return errors.New("toolchain path must be an absolute directory")
	}
	p := permissions.Canonical(raw)
	if _, err := permissions.NormalizeReadRoot(p); err != nil {
		return errors.New("toolchain path is too broad")
	}
	home, _ := os.UserHomeDir()
	for _, name := range []string{".ssh", ".gnupg", ".aws", ".config/gcloud", ".kube", ".docker", ".netrc", ".gitconfig", "Library/Keychains"} {
		credentialDir := filepath.Join(home, name)
		if permissions.Inside(p, credentialDir) || permissions.Inside(credentialDir, p) {
			return errors.New("toolchain path overlaps credential storage")
		}
	}
	for _, state := range seatbelt.ProtectedGolderPaths() {
		if permissions.Inside(p, state) || permissions.Inside(state, p) {
			return errors.New("toolchain path overlaps protected Golder state")
		}
	}
	return nil
}

func overlaps(a, b string) bool { return permissions.Inside(a, b) || permissions.Inside(b, a) }

func safeCache(path string) error {
	for _, root := range []string{"/usr", "/opt", "/etc", "/bin", "/sbin", "/System", "/Library", "/Applications", "/private/etc", "/private/var/db", "/var/lib"} {
		if overlaps(path, permissions.Canonical(root)) {
			return errors.New("writable Go cache overlaps system installation data")
		}
	}
	return nil
}
