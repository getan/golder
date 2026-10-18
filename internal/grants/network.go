// Package grants also owns the remembered network answers. They live beside
// the path table rather than inside it: a path row is keyed by a location and
// matched by walking its parents, while a network row is keyed by a host or a
// command and matched exactly. Folding the two into one file would make an old
// golder that rewrites grants.json silently drop the network rules, and would
// make every /grants listing mix two units that answer different questions.
//
// The file is deliberately separate for the same reason: a downgrade must not
// turn a remembered "yes, fetch github.com" into a parse error for the path
// table, and it must not delete a rule the user believes is still there.
package grants

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/getan/golder/internal/golderhome"
)

// NetworkKind is what a network rule names. A host rule is exact and applies
// to the fetch tools; a command rule is an exact program/subcommand shape for
// bash; a tool rule is the whole tool (the only honest key for websearch,
// whose request host depends on which backend credential is present).
type NetworkKind string

const (
	NetworkHost    NetworkKind = "host"
	NetworkCommand NetworkKind = "command"
	NetworkTool    NetworkKind = "tool"
)

// NetworkDecision is the answer for one rule lookup.
type NetworkDecision uint8

const (
	NetworkUndecided NetworkDecision = iota
	NetworkAllowed
	NetworkDenied
)

func (d NetworkDecision) String() string {
	switch d {
	case NetworkAllowed:
		return "allowed"
	case NetworkDenied:
		return "denied"
	default:
		return "undecided"
	}
}

// NetworkGrant is one remembered answer. Tool empty means it applies to every
// tool that can produce this key; a host rule is normally tool-scoped because
// "fetch github.com" is not "curl github.com".
type NetworkGrant struct {
	Kind     NetworkKind `json:"kind"`
	Key      string      `json:"key"`
	Tool     string      `json:"tool,omitempty"`
	Allow    bool        `json:"allow"`
	Lifetime Lifetime    `json:"lifetime"`
	Source   string      `json:"source,omitempty"`
}

// NetworkVerdict is the outcome of one lookup.
type NetworkVerdict struct {
	Decision NetworkDecision
	By       NetworkGrant
}

// NetworkEntries is the whole network table in its two layers.
type NetworkEntries struct {
	Remembered []NetworkGrant
	Session    []NetworkGrant
}

// All returns every row, remembered first.
func (e NetworkEntries) All() []NetworkGrant {
	out := make([]NetworkGrant, 0, len(e.Remembered)+len(e.Session))
	out = append(out, e.Remembered...)
	out = append(out, e.Session...)
	return out
}

// NetworkStore is the process table for network rules. It mirrors Store's
// layering and locking, with a different key and no parent walk.
type NetworkStore struct {
	mu      sync.RWMutex
	path    string
	saved   map[string]NetworkGrant
	session map[string]NetworkGrant
	removed map[string]bool
	loaded  bool
	warn    []string
}

// NewNetwork opens the managed network file. A missing file starts empty.
func NewNetwork(opts Options) (*NetworkStore, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	path := opts.Path
	if path == "" {
		path = golderhome.FileFrom(getenv, golderhome.NetworkGrantsFile)
	}
	s := &NetworkStore{
		path:    path,
		saved:   map[string]NetworkGrant{},
		session: map[string]NetworkGrant{},
		removed: map[string]bool{},
	}
	if path == "" {
		if opts.AllowUnpersisted {
			s.warn = append(s.warn, "network grants: no state directory; rules last for this session only")
			return s, nil
		}
		return nil, fmt.Errorf("network grants: no state directory")
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// DefaultNetworkPath returns network_grants.json in the state root.
func DefaultNetworkPath(getenv func(string) string) string {
	return golderhome.FileFrom(getenv, golderhome.NetworkGrantsFile)
}

func (s *NetworkStore) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := parseNetworkFile(data)
	if err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	s.saved = rows
	s.loaded = true
	return nil
}

// Loaded reports whether the managed file existed when opened.
func (s *NetworkStore) Loaded() bool { return s.loaded }

// Warnings returns non-fatal problems collected during load/save.
func (s *NetworkStore) Warnings() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.warn...)
}

// Entries returns both layers, sorted by kind and key.
func (s *NetworkStore) Entries() NetworkEntries {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return NetworkEntries{
		Remembered: sortedNetworkGrants(s.saved),
		Session:    sortedNetworkGrants(s.session),
	}
}

// Check answers one exact rule lookup. Session answers rank before remembered
// ones, as they do in the path table.
func (s *NetworkStore) Check(kind NetworkKind, key, tool string) NetworkVerdict {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.checkLocked(kind, key, tool)
}

func (s *NetworkStore) checkLocked(kind NetworkKind, key, tool string) NetworkVerdict {
	key = normalizeNetworkKey(kind, key)
	if key == "" {
		return NetworkVerdict{}
	}
	for _, layer := range []map[string]NetworkGrant{s.session, s.saved} {
		g, ok := layer[networkRowKey(kind, key)]
		if !ok || (g.Tool != "" && !strings.EqualFold(g.Tool, tool)) {
			continue
		}
		if g.Allow {
			return NetworkVerdict{Decision: NetworkAllowed, By: g}
		}
		return NetworkVerdict{Decision: NetworkDenied, By: g}
	}
	return NetworkVerdict{}
}

// Add records one answer. A remembered write that fails falls back to a
// session rule, exactly as the path table does, so a dialog cannot claim a
// rule was remembered when the file was not written.
func (s *NetworkStore) Add(g NetworkGrant) (NetworkGrant, error) {
	g.Kind = NetworkKind(strings.ToLower(strings.TrimSpace(string(g.Kind))))
	g.Key = normalizeNetworkKey(g.Kind, g.Key)
	g.Tool = strings.ToLower(strings.TrimSpace(g.Tool))
	if g.Key == "" {
		return g, fmt.Errorf("network rule: empty key")
	}
	if g.Lifetime == "" {
		g.Lifetime = Session
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := networkRowKey(g.Kind, g.Key)
	if g.Lifetime == Remembered {
		if s.path == "" {
			g.Lifetime = Session
			s.session[key] = g
			return g, fmt.Errorf("no network store is available; rule lasts for this session only")
		}
		old, hadOld := s.saved[key]
		s.saved[key] = g
		if err := s.saveLocked(); err != nil {
			if hadOld {
				s.saved[key] = old
			} else {
				delete(s.saved, key)
			}
			g.Lifetime = Session
			s.session[key] = g
			s.warn = append(s.warn, fmt.Sprintf("network grants: could not write %s (%v); rule lasts for this session only", s.path, err))
			return g, fmt.Errorf("could not remember network rule: %w", err)
		}
		return g, nil
	}
	g.Lifetime = Session
	s.session[key] = g
	return g, nil
}

// Remove deletes a rule from both layers.
func (s *NetworkStore) Remove(kind NetworkKind, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key = normalizeNetworkKey(kind, key)
	if key == "" {
		return false
	}
	row := networkRowKey(kind, key)
	_, inSaved := s.saved[row]
	_, inSession := s.session[row]
	if !inSaved && !inSession {
		return false
	}
	delete(s.session, row)
	if inSaved {
		delete(s.saved, row)
		s.removed[row] = true
		if err := s.saveLocked(); err != nil {
			s.warn = append(s.warn, fmt.Sprintf("network grants: could not remove %s from %s (%v)", key, s.path, err))
		}
	}
	return true
}

// Process-wide network table.
var networkProcess = struct {
	mu sync.RWMutex
	s  *NetworkStore
}{}

// OpenNetwork initializes the process network table. A failure is returned to
// the caller, which treats it like the path table's open failure: the feature
// is unavailable, not a reason to guess.
func OpenNetwork(getenv func(string) string) (warnings []string, err error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	s, err := NewNetwork(Options{Getenv: getenv, AllowUnpersisted: true})
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, s.Warnings()...)
	networkProcess.mu.Lock()
	networkProcess.s = s
	networkProcess.mu.Unlock()
	return warnings, nil
}

// SetProcessNetwork installs a table directly; tests use it.
func SetProcessNetwork(s *NetworkStore) {
	networkProcess.mu.Lock()
	networkProcess.s = s
	networkProcess.mu.Unlock()
}

// ProcessNetwork returns the process table, or nil when it was never opened.
func ProcessNetwork() *NetworkStore {
	networkProcess.mu.RLock()
	defer networkProcess.mu.RUnlock()
	return networkProcess.s
}

// NetworkCheck consults the process table.
func NetworkCheck(kind NetworkKind, key, tool string) NetworkVerdict {
	if s := ProcessNetwork(); s != nil {
		return s.Check(kind, key, tool)
	}
	return NetworkVerdict{}
}

// AddNetwork records a rule in the process table.
func AddNetwork(g NetworkGrant) (NetworkGrant, error) {
	s := ProcessNetwork()
	if s == nil {
		g.Lifetime = Session
		return g, fmt.Errorf("network grants: no process table is open")
	}
	return s.Add(g)
}

// RemoveNetwork removes a rule from the process table.
func RemoveNetwork(kind NetworkKind, key string) bool {
	s := ProcessNetwork()
	if s == nil {
		return false
	}
	return s.Remove(kind, key)
}

// AddNetworkHost validates and records a host rule. Hosts are exact: a grant
// for api.github.com does not cover github.com, and wildcards are not accepted.
func AddNetworkHost(raw, tool string, allow bool, life Lifetime, source string) (NetworkGrant, error) {
	host, err := NormalizeNetworkHost(raw)
	if err != nil {
		return NetworkGrant{}, err
	}
	return AddNetwork(NetworkGrant{Kind: NetworkHost, Key: host, Tool: tool, Allow: allow, Lifetime: life, Source: source})
}

// AddNetworkCommand validates and records a command rule.
func AddNetworkCommand(raw string, allow bool, life Lifetime, source string) (NetworkGrant, error) {
	cmd, err := NormalizeNetworkCommand(raw)
	if err != nil {
		return NetworkGrant{}, err
	}
	return AddNetwork(NetworkGrant{Kind: NetworkCommand, Key: cmd, Tool: "bash", Allow: allow, Lifetime: life, Source: source})
}

// NormalizeNetworkHost accepts a bare DNS host, lowercases it, strips a
// trailing dot, and refuses the shapes that must never become a standing
// permission: wildcards, IP literals (including metadata), single-label names,
// loopback, private ranges and the special-use suffixes used by local
// networks. The caller then offers only a per-call answer for those.
func NormalizeNetworkHost(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	h = strings.TrimSuffix(h, ".")
	if h == "" || strings.ContainsAny(h, "*/?#@:[]") {
		return "", fmt.Errorf("network rule: %q is not a bare host", raw)
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return "", fmt.Errorf("network rule: %q is a local name", raw)
	}
	if net.ParseIP(h) != nil {
		return "", fmt.Errorf("network rule: %q is an IP address", raw)
	}
	if !strings.Contains(h, ".") {
		return "", fmt.Errorf("network rule: %q is a single-label host", raw)
	}
	return h, nil
}

// commandPrograms are the program families whose subcommand is a stable enough
// key to remember. Programs that carry their own config, can run arbitrary
// commands, or can reach an arbitrary host (curl, ssh, git's ext:: transport)
// are deliberately absent: those answers may be for one call only.
var commandPrograms = map[string]bool{
	"git": true, "gh": true, "go": true, "npm": true, "pnpm": true,
	"yarn": true, "cargo": true, "pip": true, "pip3": true, "docker": true,
	"apt": true, "apt-get": true, "brew": true,
}

// NormalizeNetworkCommand reduces a simple command to "program subcommand".
// It refuses anything that is not one literal simple command: pipelines,
// separators, redirects, substitutions and dynamic words all make the key a
// promise about a shape the command does not actually have.
func NormalizeNetworkCommand(raw string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return "", fmt.Errorf("network rule: empty command")
	}
	if len(fields) > 8 {
		return "", fmt.Errorf("network rule: command is too long to remember")
	}
	for _, f := range fields {
		if strings.ContainsAny(f, ";|&$`()<>") {
			return "", fmt.Errorf("network rule: command contains shell syntax")
		}
	}
	prog := strings.ToLower(filepath.Base(fields[0]))
	if !commandPrograms[prog] {
		return "", fmt.Errorf("network rule: %q is not a command that can be remembered safely", fields[0])
	}
	sub := ""
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			continue
		}
		sub = strings.ToLower(f)
		break
	}
	if sub == "" {
		return "", fmt.Errorf("network rule: %q has no stable subcommand", raw)
	}
	return prog + " " + sub, nil
}

func normalizeNetworkKey(kind NetworkKind, key string) string {
	switch kind {
	case NetworkHost:
		h, err := NormalizeNetworkHost(key)
		if err != nil {
			return ""
		}
		return h
	case NetworkCommand:
		c, err := NormalizeNetworkCommand(key)
		if err != nil {
			return ""
		}
		return c
	case NetworkTool:
		return strings.ToLower(strings.TrimSpace(key))
	default:
		return ""
	}
}

func networkRowKey(kind NetworkKind, key string) string {
	return string(kind) + "\x00" + key
}

func sortedNetworkGrants(m map[string]NetworkGrant) []NetworkGrant {
	out := make([]NetworkGrant, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// networkFileJSON is the on-disk shape. Version is separate from grants.json
// so the two files can evolve independently.
type networkFileJSON struct {
	Version int              `json:"version"`
	Rules   []networkRowJSON `json:"rules"`
}

type networkRowJSON struct {
	Kind     NetworkKind `json:"kind"`
	Key      string      `json:"key"`
	Tool     string      `json:"tool,omitempty"`
	Allow    bool        `json:"allow"`
	Lifetime Lifetime    `json:"lifetime"`
	Source   string      `json:"source,omitempty"`
}

const networkCurrentVersion = 1

func parseNetworkFile(data []byte) (map[string]NetworkGrant, error) {
	var f networkFileJSON
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Version > networkCurrentVersion {
		return nil, fmt.Errorf("network grants: file version %d is newer than this golder supports (%d)", f.Version, networkCurrentVersion)
	}
	out := map[string]NetworkGrant{}
	for _, r := range f.Rules {
		if r.Lifetime != Remembered {
			return nil, fmt.Errorf("network grants: %q is marked %q in a remembered file", r.Key, r.Lifetime)
		}
		g := NetworkGrant{Kind: r.Kind, Key: normalizeNetworkKey(r.Kind, r.Key), Tool: r.Tool, Allow: r.Allow, Lifetime: Remembered, Source: r.Source}
		if g.Key == "" {
			return nil, fmt.Errorf("network grants: invalid %s rule %q", r.Kind, r.Key)
		}
		out[networkRowKey(g.Kind, g.Key)] = g
	}
	return out, nil
}

// saveLocked writes the network file atomically, merging rows another process
// wrote while this one was deciding. The lock is held only for the write.
func (s *NetworkStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	if data, err := os.ReadFile(s.path); err == nil {
		if onDisk, perr := parseNetworkFile(data); perr == nil {
			for key, g := range onDisk {
				if s.removed[key] {
					continue
				}
				if _, mine := s.saved[key]; !mine {
					s.saved[key] = g
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	f := networkFileJSON{Version: networkCurrentVersion}
	for _, g := range sortedNetworkGrants(s.saved) {
		f.Rules = append(f.Rules, networkRowJSON{Kind: g.Kind, Key: g.Key, Tool: g.Tool, Allow: g.Allow, Lifetime: Remembered, Source: g.Source})
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".network-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}
