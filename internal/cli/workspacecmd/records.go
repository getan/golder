package workspacecmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/privatepath"
)

// sessionRecordVersion is the on-disk format of session.json. It is separate
// from the daemon's own version so a record can be read (and reported) by a
// build that cannot start its daemon.
const sessionRecordVersion = 1

const sessionRecordLimit = 64 << 10

// SessionRecord is one named session's durable identity. It deliberately holds
// no secret: the socket path is a filesystem name, and the ownership proof
// stays in the daemon's private owner record.
type SessionRecord struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	Cwd       string `json:"cwd"`
	Socket    string `json:"socket"`
	SocketDir string `json:"socket_dir,omitempty"`
	Config    string `json:"config"`
	StateDir  string `json:"state_dir"`
	Log       string `json:"log"`
	CreatedAt string `json:"created_at"`
}

// SaveSession writes one named session's record atomically. It never writes a
// legacy session; those have no record and are addressed only by --legacy.
func SaveSession(opts Options) error {
	if opts.Legacy {
		return errors.New("workspace: a legacy session has no record to write")
	}
	if opts.ManifestPath == "" {
		return errors.New("workspace: session record path is empty")
	}
	if err := validateName(opts.Session); err != nil {
		return err
	}
	if err := EnsureIdentityDirs(opts); err != nil {
		return err
	}
	created := opts.CreatedAt
	if existing, ok, err := loadRecord(opts); err != nil {
		return err
	} else if ok {
		if existing.CreatedAt != "" {
			created = existing.CreatedAt
		}
	}
	if created == "" {
		created = time.Now().UTC().Format(time.RFC3339)
	}
	record := SessionRecord{
		Version:   sessionRecordVersion,
		Name:      opts.Session,
		Cwd:       opts.Cwd,
		Socket:    opts.Socket,
		SocketDir: opts.SocketDir,
		Config:    opts.ConfigPath,
		StateDir:  opts.StateDir,
		Log:       opts.LogFile,
		CreatedAt: created,
	}
	if err := validateRecord(opts, record); err != nil {
		return err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("workspace: encode session record: %w", err)
	}
	f, err := os.CreateTemp(opts.SessionDir, ".session-")
	if err != nil {
		return fmt.Errorf("workspace: create session record: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(append(body, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, opts.ManifestPath); err != nil {
		return fmt.Errorf("workspace: save session record: %w", err)
	}
	return nil
}

// ReloadSession re-reads a record after a caller has acquired the session lock.
// The first resolve happened without that lock, so a concurrent start could
// have created or rewritten the record in between; the backend must not decide
// "this is new" from the earlier answer.
func ReloadSession(opts Options) (Options, error) {
	record, exists, err := loadRecord(opts)
	if err != nil {
		return Options{}, err
	}
	if !exists {
		opts.Exists = false
		return opts, nil
	}
	opts.Exists = true
	opts.CreatedAt = record.CreatedAt
	if opts.SocketExplicit && opts.Socket != record.Socket {
		return Options{}, fmt.Errorf("workspace: session %q is recorded with socket %q, not %q", opts.Session, record.Socket, opts.Socket)
	}
	if opts.ConfigExplicit && opts.ConfigPath != record.Config {
		return Options{}, fmt.Errorf("workspace: session %q is recorded with config %q, not %q", opts.Session, record.Config, opts.ConfigPath)
	}
	if opts.StateDirExplicit && opts.StateDir != record.StateDir {
		return Options{}, fmt.Errorf("workspace: session %q is recorded with state directory %q, not %q", opts.Session, record.StateDir, opts.StateDir)
	}
	if !opts.CwdExplicit {
		opts.Cwd = record.Cwd
	}
	opts.Project = opts.Cwd
	if record.Socket != "" {
		opts.Socket = record.Socket
		opts.SocketDir = record.SocketDir
	}
	if record.Config != "" {
		opts.ConfigPath = record.Config
	}
	if record.StateDir != "" {
		opts.StateDir = record.StateDir
	}
	if record.Log != "" {
		opts.LogFile = record.Log
	}
	return opts, nil
}

// loadRecord reads opts.ManifestPath. A missing file is (zero, false, nil), not
// an error: that is how every command discovers a name nobody has used yet.
func loadRecord(opts Options) (SessionRecord, bool, error) {
	if opts.ManifestPath == "" {
		return SessionRecord{}, false, nil
	}
	if err := CheckIdentity(opts); err != nil {
		return SessionRecord{}, false, err
	}
	if err := checkDirectories(opts.Home, opts.SessionDir); err != nil {
		return SessionRecord{}, false, err
	}
	// Reject accidental FIFOs/devices before the private reader opens them.
	// That reader verifies the opened descriptor again, including ownership.
	st, statErr := os.Lstat(opts.ManifestPath)
	if errors.Is(statErr, os.ErrNotExist) {
		return SessionRecord{}, false, nil
	}
	if statErr != nil {
		return SessionRecord{}, false, statErr
	}
	if !st.Mode().IsRegular() {
		return SessionRecord{}, false, fmt.Errorf("workspace: session record %q is not a regular file", opts.ManifestPath)
	}
	data, err := privatepath.ReadFile(opts.ManifestPath, 0o600, sessionRecordLimit+1)
	if errors.Is(err, os.ErrNotExist) {
		return SessionRecord{}, false, nil
	}
	if err != nil {
		return SessionRecord{}, false, fmt.Errorf("workspace: read session record %q: %w", opts.ManifestPath, err)
	}
	if len(data) > sessionRecordLimit {
		return SessionRecord{}, false, fmt.Errorf("workspace: session record %q exceeds the %d-byte limit", opts.ManifestPath, sessionRecordLimit)
	}
	var record SessionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return SessionRecord{}, false, fmt.Errorf("workspace: session record %q is not valid JSON: %w", opts.ManifestPath, err)
	}
	if record.Version != sessionRecordVersion {
		return SessionRecord{}, false, fmt.Errorf("workspace: session record %q has version %d; this golder understands version %d", opts.ManifestPath, record.Version, sessionRecordVersion)
	}
	if record.Name != opts.Session {
		return SessionRecord{}, false, fmt.Errorf("workspace: session record %q names %q, not %q", opts.ManifestPath, record.Name, opts.Session)
	}
	if err := validateRecord(opts, record); err != nil {
		return SessionRecord{}, false, err
	}
	return record, true, nil
}

func validateRecord(opts Options, r SessionRecord) error {
	for key, path := range map[string]string{"cwd": r.Cwd, "socket": r.Socket, "config": r.Config, "state_dir": r.StateDir, "log": r.Log} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("workspace: session %q has an invalid absolute %s path", opts.Session, key)
		}
	}
	if len(r.Socket) > maxSocketPath || r.Log != filepath.Join(opts.SessionDir, logFileName) {
		return fmt.Errorf("workspace: session %q has invalid socket/log paths", opts.Session)
	}
	if r.SocketDir != "" && r.SocketDir != filepath.Dir(r.Socket) {
		return fmt.Errorf("workspace: session %q has a mismatched socket directory", opts.Session)
	}
	if _, err := time.Parse(time.RFC3339, r.CreatedAt); err != nil {
		return fmt.Errorf("workspace: session %q has an invalid creation time", opts.Session)
	}
	return nil
}

// CheckIdentity prevents a caller or a corrupt record from retargeting a named
// session's removable directory. Config and state can be explicit overrides;
// identity, manifest and log paths cannot.
func CheckIdentity(opts Options) error {
	if opts.Legacy || !filepath.IsAbs(opts.Home) {
		return errors.New("workspace: expected a global named session under an absolute state root")
	}
	if err := validateName(opts.Session); err != nil {
		return err
	}
	want := filepath.Join(opts.Home, workspaceDirName, globalSessionsDirName, opts.Session)
	if opts.SessionDir != want || opts.ManifestPath != filepath.Join(want, manifestFileName) || opts.LogFile != filepath.Join(want, logFileName) {
		return errors.New("workspace: named session paths do not match its state root and name")
	}
	return nil
}

// EnsureIdentityDirs walks each component owned by golder separately, refusing
// symlinks before creating anything underneath them. Runtime locks live beside
// sessions, not inside a directory `rm` can remove.
func EnsureIdentityDirs(opts Options) error {
	if err := CheckIdentity(opts); err != nil {
		return err
	}
	for _, path := range []string{opts.Home, filepath.Join(opts.Home, workspaceDirName), filepath.Dir(opts.SessionDir), opts.SessionDir} {
		if err := privatepath.EnsureDir(path); err != nil {
			return err
		}
	}
	return nil
}

// SessionLockPath is stable across removal/recreation of the session record.
func SessionLockPath(opts Options) string {
	return filepath.Join(opts.Home, workspaceDirName, ".locks", opts.Session+".lock")
}

// checkDirectories is the read-only counterpart of EnsureIdentityDirs. A lookup
// never creates/chmods state, and never follows a link in the known subtree.
func checkDirectories(home, end string) error {
	rel, err := filepath.Rel(home, end)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("workspace: state directory escapes its home")
	}
	path := home
	parts := append([]string{""}, strings.Split(rel, string(filepath.Separator))...)
	for _, part := range parts {
		path = filepath.Join(path, part)
		st, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace: %s is a symbolic link", path)
		}
		if !st.IsDir() {
			return fmt.Errorf("workspace: %s is not a directory", path)
		}
	}
	return nil
}

// applyRecord copies a record onto Options. Callers that must preserve explicit
// flags use ReloadSession instead; discovery and resolve use this after they
// have already decided precedence.
func applyRecord(opts *Options, record SessionRecord) {
	opts.Exists = true
	opts.CreatedAt = record.CreatedAt
	if record.Cwd != "" {
		opts.Cwd = record.Cwd
	}
	if record.Socket != "" {
		opts.Socket = record.Socket
		opts.SocketDir = record.SocketDir
	}
	if record.Config != "" {
		opts.ConfigPath = record.Config
	}
	if record.StateDir != "" {
		opts.StateDir = record.StateDir
	}
	if record.Log != "" {
		opts.LogFile = record.Log
	}
	opts.Project = opts.Cwd
}

// Discover lists every named session and every legacy session directory under
// home. It reads records only: no daemon is started, no socket is created and
// no saved start directory has to exist.
func Discover(home string) ([]Options, error) {
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	var out []Options

	globalRoot := filepath.Join(home, workspaceDirName, globalSessionsDirName)
	if err := checkDirectories(home, globalRoot); err != nil {
		return nil, err
	}
	if entries, err := os.ReadDir(globalRoot); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			name := entry.Name()
			if validateName(name) != nil {
				continue
			}
			opts, manifest := globalOptions(home, name)
			record, exists, err := loadRecord(opts)
			if err != nil {
				opts.DiscoveryError = err.Error()
				out = append(out, opts)
				continue
			}
			if !exists {
				continue
			}
			applyRecord(&opts, record)
			opts.ManifestPath = manifest
			if opts.Socket == "" {
				opts.Socket, opts.SocketDir, err = namedSocketPath(os.Getenv, home, name, "", "")
				if err != nil {
					return nil, err
				}
			}
			out = append(out, opts)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("workspace: list sessions: %w", err)
	}

	legacyRoot := filepath.Join(home, workspaceDirName)
	if entries, err := os.ReadDir(legacyRoot); err == nil {
		for _, entry := range entries {
			if entry.Name() == globalSessionsDirName || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if !isLegacyID(entry.Name()) {
				continue
			}
			sessionsDir := filepath.Join(legacyRoot, entry.Name(), sessionsDirName)
			if err := checkDirectories(home, sessionsDir); err != nil {
				return nil, err
			}
			sessions, err := os.ReadDir(sessionsDir)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("workspace: list legacy sessions: %w", err)
			}
			for _, sessionEntry := range sessions {
				if !sessionEntry.IsDir() || sessionEntry.Type()&os.ModeSymlink != 0 {
					continue
				}
				name := sessionEntry.Name()
				if validateSession(name) != nil {
					continue
				}
				opts := legacyOptions(home, entry.Name(), name)
				if record, found, err := findLegacyOwner(opts.SessionDir, name); err != nil {
					opts.DiscoveryError = err.Error()
				} else if found {
					opts.Cwd = record.Project
					opts.Project = record.Project
					opts.Socket = record.Socket
					opts.ConfigPath = record.Config
					opts.StateDir = record.StateDir
					opts.LogFile = record.Log
				} else {
					opts.DiscoveryError = "legacy session has no ownership record; its old endpoint is unknown"
				}
				out = append(out, opts)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("workspace: list legacy sessions: %w", err)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Legacy != out[j].Legacy {
			return !out[i].Legacy
		}
		return out[i].Selector() < out[j].Selector()
	})
	return out, nil
}

// globalOptions is the one path layout for a named session. Keeping it in one
// function is what stops resolve and list from disagreeing.
func globalOptions(home, name string) (Options, string) {
	sessionDir := filepath.Join(home, workspaceDirName, globalSessionsDirName, name)
	manifest := filepath.Join(sessionDir, manifestFileName)
	return Options{
		Session:      name,
		ManifestPath: manifest,
		Paths: Paths{
			Home:       home,
			ProjectDir: sessionDir,
			ConfigPath: filepath.Join(sessionDir, golderhome.ConfigFile),
			SessionDir: sessionDir,
			StateDir:   filepath.Join(sessionDir, stateDirName),
			LogFile:    filepath.Join(sessionDir, logFileName),
		},
	}, manifest
}

func legacyOptions(home, id, session string) Options {
	projectDir := filepath.Join(home, workspaceDirName, id)
	sessionDir := filepath.Join(projectDir, sessionsDirName, session)
	return Options{
		Session: session,
		Legacy:  true,
		Exists:  true,
		Paths: Paths{
			Home:       home,
			ProjectID:  id,
			ProjectDir: projectDir,
			ConfigPath: filepath.Join(projectDir, golderhome.ConfigFile),
			SessionDir: sessionDir,
			StateDir:   filepath.Join(sessionDir, stateDirName),
			LogFile:    filepath.Join(sessionDir, logFileName),
		},
	}
}

func isLegacyID(name string) bool {
	if len(name) != 12 {
		return false
	}
	for _, r := range name {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// legacyOwner is the public half of an old daemon's owner record. The token,
// instance and pid are deliberately not decoded: listing a session must never
// turn state into authority.
type legacyOwner struct {
	Socket   string `json:"socket"`
	Control  string `json:"control"`
	Session  string `json:"session"`
	Project  string `json:"project"`
	Config   string `json:"config"`
	StateDir string `json:"state_dir"`
	Log      string `json:"log"`
}

// findLegacyOwner reads the old ownership record without adopting it. The
// backend reads the full record again and proves the token before attaching or
// stopping.
func findLegacyOwner(sessionDir, session string) (legacyOwner, bool, error) {
	entries, err := os.ReadDir(sessionDir)
	if errors.Is(err, os.ErrNotExist) {
		return legacyOwner{}, false, nil
	}
	if err != nil {
		return legacyOwner{}, false, fmt.Errorf("workspace: read legacy session %q: %w", sessionDir, err)
	}
	var result legacyOwner
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "owner-") || !strings.HasSuffix(name, ".json") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if !entry.Type().IsRegular() {
			return result, false, fmt.Errorf("workspace: legacy ownership record %q is not a regular file", name)
		}
		data, err := privatepath.ReadFile(filepath.Join(sessionDir, name), 0o600, sessionRecordLimit)
		if err != nil {
			return result, false, err
		}
		var record legacyOwner
		if json.Unmarshal(data, &record) != nil || record.Session != session || record.Socket == "" {
			return result, false, fmt.Errorf("workspace: invalid legacy ownership record %q", name)
		}
		sum := sha256.Sum256([]byte(record.Socket))
		if name != "owner-"+hex.EncodeToString(sum[:8])+".json" || !filepath.IsAbs(record.Socket) || !filepath.IsAbs(record.Project) || !filepath.IsAbs(record.Config) || !filepath.IsAbs(record.StateDir) || !filepath.IsAbs(record.Log) {
			return result, false, fmt.Errorf("workspace: legacy ownership record %q does not match its endpoint", name)
		}
		if found {
			return result, false, errors.New("workspace: multiple legacy endpoints for this session; refusing to choose one")
		}
		result, found = record, true
	}
	return result, found, nil
}

// Selector is the explicit, unambiguous CLI target shown by list. Legacy names
// are qualified by the old project id, since two projects may share a name.
func (o Options) Selector() string {
	if o.Legacy {
		return o.ProjectID + "/" + o.Session
	}
	return o.Session
}
