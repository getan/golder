package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/extension"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/redact"
	"github.com/getan/golder/internal/seatbelt"
)

const outputLimit = 1 << 20

// DisplayText is for terminal-facing data, not for rewriting executable input.
func DisplayText(s string, limit int) string { return clean(s, limit) }

// logBuffer is also the CLI stdout sink: it always drains, never blocks a child
// merely because output is larger than the retained limit.
type logBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	limit := b.limit
	if limit == 0 {
		limit = 64 << 10
	}
	if len(b.data)+n > limit {
		b.truncated = true
	}
	if n >= limit {
		b.data = append(b.data[:0], p[n-limit:]...)
	} else {
		if excess := len(b.data) + n - limit; excess > 0 {
			b.data = b.data[excess:]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := string(b.data)
	if b.truncated {
		s = "[output truncated]\n" + s
	}
	return clean(s, outputLimit)
}

func clean(s string, limit int) string {
	// Strip full ANSI/OSC sequences as well as other control characters.
	var out strings.Builder
	state := byte(0)
	for _, r := range s {
		if state == 1 {
			if r == '[' {
				state = 2
			} else if r == ']' {
				state = 3
			} else {
				state = 0
			}
			continue
		}
		if state == 2 {
			if r >= 0x40 && r <= 0x7e {
				state = 0
			}
			continue
		}
		if state == 3 {
			if r == 7 {
				state = 0
			} else if r == 27 {
				state = 4
			}
			continue
		}
		if state == 4 {
			if r == '\\' {
				state = 0
			} else {
				state = 3
			}
			continue
		}
		if r == 27 {
			state = 1
			continue
		}
		if r >= 32 && !(r >= 127 && r < 160) || r == '\n' || r == '\t' {
			out.WriteRune(r)
		}
		if out.Len() >= limit {
			break
		}
	}
	s = out.String()
	if r := redact.Default(); r != nil {
		s = r.Text(redact.SeamSession, "", s)
	}
	return s
}

func resolveValue(s string) (string, error) {
	var failed bool
	s = os.Expand(s, func(key string) string {
		if strings.HasPrefix(key, "credential:") {
			v, e := provider.ResolveCredentialReference(provider.CredentialFilePath(), strings.TrimPrefix(key, "credential:"))
			if e != nil {
				failed = true
			}
			return v
		}
		v, ok := os.LookupEnv(key)
		if !ok {
			failed = true
		}
		return v
	})
	if failed {
		return "", errors.New("authentication required: missing environment variable or credential reference")
	}
	return s, nil
}

func processEnv(c SourceConfig, home string) ([]string, error) {
	allow := map[string]string{}
	for _, pattern := range c.EnvPassthrough {
		for _, pair := range os.Environ() {
			name, value, ok := strings.Cut(pair, "=")
			if match, _ := filepath.Match(pattern, name); ok && match && !strings.HasPrefix(name, "GOLDER_") {
				allow[name] = value
			}
		}
	}
	for name, value := range c.Env {
		if strings.HasPrefix(name, "GOLDER_") {
			return nil, errors.New("GOLDER_* cannot be injected into an extension")
		}
		v, e := resolveValue(value)
		if e != nil {
			return nil, e
		}
		allow[name] = v
	}
	allow["HOME"] = home
	allow["CFFIXED_USER_HOME"] = home
	allow["TMPDIR"] = home
	allow["XDG_CONFIG_HOME"] = filepath.Join(home, "config")
	allow["XDG_DATA_HOME"] = filepath.Join(home, "data")
	allow["XDG_CACHE_HOME"] = filepath.Join(home, "cache")
	allow["XDG_STATE_HOME"] = filepath.Join(home, "state")
	paths := make([]string, 0, len(c.Executables))
	for _, raw := range c.Executables {
		p := expandPath(raw)
		if canonical, err := filepath.EvalSymlinks(p); err == nil {
			p = canonical
		}
		paths = append(paths, p)
	}
	encoded, _ := json.Marshal(paths)
	allow[extension.ExecutablePathsEnv] = string(encoded) // host-owned; operator env cannot inject GOLDER_*
	return provider.ScrubEnv(os.Environ(), allow), nil
}

func prepareCommand(ctx context.Context, c SourceConfig, cwd string, argv []string, logs io.Writer) (*exec.Cmd, func(), error) {
	if len(argv) == 0 {
		return nil, nil, errors.New("missing executable")
	}
	program, e := resolvedProgram(argv[0])
	if e != nil {
		return nil, nil, errors.New("extension executable not found")
	}
	tmp, e := os.MkdirTemp("", "golder-extension-*")
	if e != nil {
		return nil, nil, e
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	home := tmp
	var state *seatbelt.ExtensionState
	if c.StateDir {
		if c.stateHome == "" {
			cleanup()
			return nil, nil, errors.New("persistent HOME has no source identity")
		}
		state, e = seatbelt.NewExtensionState(c.stateHome)
		if e != nil {
			cleanup()
			return nil, nil, e
		}
		if e = privateHome(c.stateHome); e != nil {
			cleanup()
			return nil, nil, e
		}
		unlock, err := lockState(filepath.Join(filepath.Dir(c.stateHome), ".lock"))
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		removeTemp := cleanup
		cleanup = func() { unlock(); removeTemp() }
		home = c.stateHome
	}
	// The state root is intentionally unreadable to children. Stage only the
	// approved executable, never its directory (which also contains secrets).
	for _, root := range seatbelt.ProtectedGolderPaths() {
		real, _ := filepath.EvalSymlinks(root)
		if real == "" {
			real = root
		}
		rel, err := filepath.Rel(real, program)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			in, err := os.Open(program)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			staged := filepath.Join(tmp, filepath.Base(program))
			out, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
			if err == nil {
				_, err = io.Copy(out, in)
				ce := out.Close()
				if err == nil {
					err = ce
				}
			}
			_ = in.Close()
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			program = staged
			break
		}
	}
	env, e := processEnv(c, home)
	if e != nil {
		cleanup()
		return nil, nil, e
	}
	for i, value := range env {
		if strings.HasPrefix(value, "TMPDIR=") {
			env[i] = "TMPDIR=" + tmp
		}
	}
	dir := cwd
	if c.Dir != "" {
		dir = expandPath(c.Dir)
		if !filepath.IsAbs(dir) {
			cleanup()
			return nil, nil, errors.New("extension dir must be absolute")
		}
	}
	runner := seatbelt.New(cwd)
	if runner == nil {
		cleanup()
		return nil, nil, errors.New("extension sandbox unavailable; refusing bare execution")
	}
	policy := seatbelt.CallPolicy{Network: c.Network, ReadFiles: []string{program}, WriteDirs: []string{tmp}, ExtensionState: state}
	for _, dependency := range c.Executables {
		if _, err := os.Stat(expandPath(dependency)); os.IsNotExist(err) {
			continue
		}
		p, err := resolvedProgram(dependency)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		for _, root := range seatbelt.ProtectedGolderPaths() {
			rel, _ := filepath.Rel(root, p)
			if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				cleanup()
				return nil, nil, errors.New("install dependency executables outside the protected state root")
			}
		}
		policy.ReadFiles = append(policy.ReadFiles, p)
	}
	seatbelt.RefusedPaths(&policy)
	wrapped, release, e := runner.SandboxCall(append([]string{program}, argv[1:]...), dir, policy)
	if e != nil {
		cleanup()
		return nil, nil, e
	}
	cmd := exec.CommandContext(ctx, wrapped[0], wrapped[1:]...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stderr = logs
	cmd.WaitDelay = time.Second
	configureProcess(cmd)
	var once sync.Once
	return cmd, func() { once.Do(func() { cleanupCommand(cmd, release, cleanup) }) }, nil
}

func cleanupCommand(cmd *exec.Cmd, release, cleanup func()) {
	killProcess(cmd)
	// bubblewrap creates no profile, so its successful SandboxCall returns
	// a nil release callback. Temporary extension state still needs cleanup.
	if release != nil {
		release()
	}
	cleanup()
}
