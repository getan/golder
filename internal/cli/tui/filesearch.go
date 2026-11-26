// This file implements `@` file completion for the full-screen TUI, mirroring
// codex's file-search popup: typing `@` starts a path token, the popup lists
// matching files and directories under the session's working directory, the
// arrow keys move the highlight, and Tab/Enter insert the selected path.
//
// The popup is deliberately local-only. Codex also folds skills, plugins and
// connectors into the same mention popup; golder's version searches the local
// filesystem and nothing else.
//
// The index is built off the tea loop (loadFileIndexCmd) so opening the popup
// on a large repository cannot stall input. The walker prefers `rg --files`
// because it already honors .gitignore and the repository's ignore rules —
// golder's grep/find tools use the same binary. When ripgrep is unavailable it
// falls back to a bounded filepath.WalkDir that skips build/VCS directories.
package tui

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	// fileMenuLimit caps the ranked candidates kept for navigation. The popup
	// shows maxMenuRows at a time and scrolls a window around the highlight.
	fileMenuLimit = 50

	// fileIndexMaxEntries bounds the in-memory index so a mistaken `@` in a
	// huge home directory cannot allocate without limit. The index is only a
	// convenience list of names; it is never read for file contents.
	fileIndexMaxEntries = 30000

	// fileIndexTimeout bounds the ripgrep walk. A timed-out or failed rg run
	// falls back to the pure-Go walker, which is slower but self-contained.
	fileIndexTimeout = 8 * time.Second
)

// fileEntry is one indexed path, relative to the search root and always
// slash-separated for stable matching and display on every platform.
type fileEntry struct {
	Path string
	Dir  bool
}

// fileMatch is one ranked search result. Higher Score sorts first; ties break
// by path so the list is deterministic across runs.
type fileMatch struct {
	fileEntry
	Score int
}

// atToken is the `@token` under the cursor. Start/End are byte offsets into the
// editor buffer; Key identifies the exact token occurrence so Esc can dismiss
// one token without suppressing completion for every later `@`.
type atToken struct {
	Start int
	End   int
	Query string
	Text  string
	Key   string
}

// fileIndexMsg carries the asynchronously built file index back to the model.
type fileIndexMsg struct {
	root    string
	entries []fileEntry
	err     error
}

// fileMenu is the `@` completion popup state. It owns the cached index, the
// current query and the highlighted row. It is inactive whenever the caret is
// not on an `@token`.
type fileMenu struct {
	theme Theme

	active   bool
	loading  bool
	loadErr  string
	root     string
	indexed  bool
	entries  []fileEntry
	query    string
	matches  []fileMatch
	total    int
	selected int

	// tokenStart/tokenEnd delimit the active @token in the editor buffer and
	// tokenKey identifies its exact occurrence for dismissal.
	tokenStart int
	tokenEnd   int
	tokenKey   string
	dismissed  string
}

// newFileMenu builds an inactive file menu bound to the theme used for rows.
func newFileMenu(theme Theme) fileMenu { return fileMenu{theme: theme} }

// refresh recomputes the menu from the editor buffer and caret. It reports
// whether the caller should start an asynchronous index load. The menu stays
// active for a bare "@" (an empty query) so the popup can show its prompt.
func (fm *fileMenu) refresh(buffer string, caret int, root string) (needLoad bool) {
	if fm.root != root {
		fm.root = root
		fm.entries = nil
		fm.indexed = false
		fm.loading = false
		fm.loadErr = ""
		fm.query = ""
		fm.matches = nil
		fm.total = 0
		fm.selected = 0
		fm.dismissed = ""
	}

	tok, ok := currentAtToken(buffer, caret)
	if !ok {
		fm.close()
		fm.dismissed = ""
		return false
	}
	if fm.dismissed != "" {
		if fm.dismissed == tok.Key {
			fm.close()
			return false
		}
		fm.dismissed = ""
	}

	fm.active = true
	fm.tokenStart, fm.tokenEnd, fm.tokenKey = tok.Start, tok.End, tok.Key
	if tok.Query == "" {
		fm.query = ""
		fm.matches = nil
		fm.total = 0
		fm.selected = 0
		return false
	}
	if tok.Query != fm.query {
		fm.query = tok.Query
		fm.selected = 0
		fm.recompute()
	}
	if !fm.indexed && !fm.loading {
		fm.loading = true
		fm.loadErr = ""
		return true
	}
	return false
}

// setIndex installs a completed index (or its error) and re-filters the
// current query. The index is cached even when the popup has already closed.
func (fm *fileMenu) setIndex(entries []fileEntry, err error) {
	fm.entries = entries
	fm.indexed = true
	fm.loading = false
	if err != nil {
		fm.loadErr = err.Error()
	} else {
		fm.loadErr = ""
	}
	fm.recompute()
}

// recompute ranks the cached index against the current query. Only the top
// fileMenuLimit candidates are retained; total keeps the full match count for
// the empty-state message.
func (fm *fileMenu) recompute() {
	if fm.query == "" {
		fm.matches = nil
		fm.total = 0
		fm.selected = 0
		return
	}
	matches, total := rankFileEntries(fm.entries, fm.query, fileMenuLimit)
	fm.matches = matches
	fm.total = total
	if fm.selected >= len(fm.matches) || fm.selected < 0 {
		fm.selected = 0
	}
}

// close deactivates the popup while keeping the index cache for the next `@`.
func (fm *fileMenu) close() {
	fm.active = false
	fm.query = ""
	fm.matches = nil
	fm.total = 0
	fm.selected = 0
	fm.tokenStart, fm.tokenEnd, fm.tokenKey = 0, 0, ""
}

// dismiss closes the popup for the exact token under the cursor. A later edit
// changes the token key and lets completion reopen.
func (fm *fileMenu) dismiss() {
	if fm.tokenKey != "" {
		fm.dismissed = fm.tokenKey
	}
	fm.close()
}

// moveUp/moveDown cycle the highlight, wrapping at the ends like the slash menu.
func (fm *fileMenu) moveUp() {
	if n := len(fm.matches); n > 0 {
		fm.selected--
		if fm.selected < 0 {
			fm.selected = n - 1
		}
	}
}

func (fm *fileMenu) moveDown() {
	if n := len(fm.matches); n > 0 {
		fm.selected++
		if fm.selected >= n {
			fm.selected = 0
		}
	}
}

// selection returns the highlighted path ready to insert. Directories keep a
// trailing slash so the user can continue typing inside them, and a path with
// whitespace is double-quoted (codex does the same for its local file picker).
func (fm fileMenu) selection() (string, bool) {
	if fm.selected < 0 || fm.selected >= len(fm.matches) {
		return "", false
	}
	p := fm.matches[fm.selected].Path
	if fm.matches[fm.selected].Dir && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	if strings.ContainsAny(p, " \t") && !strings.Contains(p, `"`) {
		p = `"` + p + `"`
	}
	return p, true
}

// rows reports how many terminal rows the popup occupies, so relayout can
// reserve them out of the transcript. view draws exactly the same lines.
func (fm fileMenu) rows(width int) int {
	if !fm.active {
		return 0
	}
	return len(fm.lines(width))
}

// view renders the popup above the composer. It returns "" while inactive.
func (fm fileMenu) view(width int) string {
	if !fm.active {
		return ""
	}
	return strings.Join(fm.lines(width), "\n")
}

// lines builds the popup's display rows. Empty/loading/error states occupy one
// row; a result list scrolls a window around the highlight. Every state ends
// with one dim key hint so the interaction is discoverable at the moment it
// matters.
func (fm fileMenu) lines(width int) []string {
	rowWidth := menuRowWidth(width)
	hint := fm.theme.System.Render(ansi.Truncate("  ↑↓ select · Tab/Enter insert · Esc close", rowWidth, "…"))
	state := func(text string) []string {
		return []string{
			fm.theme.System.Render(ansi.Truncate("  "+text, rowWidth, "…")),
			hint,
		}
	}

	switch {
	case fm.query == "":
		return state("type a file name to search…")
	case fm.loading && !fm.indexed:
		return state("indexing files…")
	case fm.loadErr != "":
		return state("file search unavailable: " + fm.loadErr)
	case len(fm.matches) == 0:
		msg := "no files match " + fm.query
		if fm.total > 0 {
			msg = fmt.Sprintf("%d files match %s", fm.total, fm.query)
		}
		return state(msg)
	}

	start, end := fm.window()
	out := make([]string, 0, end-start+1)
	for i := start; i < end; i++ {
		p := fm.matches[i].Path
		if fm.matches[i].Dir {
			p += "/"
		}
		if i == fm.selected {
			out = append(out, fm.theme.Accent.Render(ansi.Truncate("› "+p, rowWidth, "…")))
			continue
		}
		out = append(out, fm.theme.System.Render(ansi.Truncate("  "+p, rowWidth, "…")))
	}
	out = append(out, hint)
	return out
}

// window returns the [start,end) result slice to draw, scrolled so the
// highlight stays visible when more than maxMenuRows candidates match.
func (fm fileMenu) window() (int, int) {
	n := len(fm.matches)
	if n <= maxMenuRows {
		return 0, n
	}
	start := fm.selected - maxMenuRows + 1
	if start < 0 {
		start = 0
	}
	if start > n-maxMenuRows {
		start = n - maxMenuRows
	}
	return start, start + maxMenuRows
}

// currentAtToken finds the `@token` that the caret is on or immediately after.
// A token is a whitespace-delimited segment beginning with '@'. The query is
// the whole segment after the sigil, independent of where the caret sits inside
// it. `@image:` is deliberately excluded: golder's image-reference syntax uses
// that spelling, and the file picker must not hijack it.
func currentAtToken(buffer string, caret int) (atToken, bool) {
	if caret < 0 {
		caret = 0
	}
	if caret > len(buffer) {
		caret = len(buffer)
	}
	// The caret may be inside a token or immediately after its last character.
	// Resolve the segment from the character under/left of the caret, then
	// expand to whitespace boundaries.
	probe := caret
	if probe == len(buffer) || atTokenSpace(buffer[probe]) {
		if probe == 0 || atTokenSpace(buffer[probe-1]) {
			return atToken{}, false
		}
		probe--
	}
	start := probe
	for start > 0 && !atTokenSpace(buffer[start-1]) {
		start--
	}
	end := probe + 1
	for end < len(buffer) && !atTokenSpace(buffer[end]) {
		end++
	}
	text := buffer[start:end]
	if !strings.HasPrefix(text, "@") {
		return atToken{}, false
	}
	query := text[1:]
	if strings.HasPrefix(query, "image:") {
		return atToken{}, false
	}
	return atToken{
		Start: start,
		End:   end,
		Query: query,
		Text:  text,
		Key:   fmt.Sprintf("%d:%s", start, text),
	}, true
}

// atTokenSpace reports whether b is a token separator. Newlines separate
// tokens too: a file query never spans composer lines.
func atTokenSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// rankFileEntries scores every indexed path against query and returns the top
// limit matches plus the total number of paths that matched.
func rankFileEntries(entries []fileEntry, query string, limit int) ([]fileMatch, int) {
	q := []rune(strings.ToLower(query))
	if len(q) == 0 {
		return nil, 0
	}
	matches := make([]fileMatch, 0, min(len(entries), limit))
	for _, e := range entries {
		if score, ok := fuzzyFileScore(q, e); ok {
			matches = append(matches, fileMatch{fileEntry: e, Score: score})
		}
	}
	total := len(matches)
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Path < matches[j].Path
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, total
}

// fuzzyFileScore performs a case-insensitive subsequence match and scores the
// result. The ranking is intentionally simple (nucleo is the reference, but a
// local list of paths does not need its full matcher): segment-boundary and
// consecutive matches score higher, basename hits beat directory hits, and
// shorter paths win ties.
func fuzzyFileScore(query []rune, e fileEntry) (int, bool) {
	path := []rune(strings.ToLower(e.Path))
	if len(query) > len(path) {
		return 0, false
	}
	baseStart := 0
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			baseStart = i + 1
			break
		}
	}

	score := 0
	qi := 0
	prev := -2
	for i := 0; i < len(path) && qi < len(query); i++ {
		if path[i] != query[qi] {
			continue
		}
		score += 1
		if i == 0 || isPathBoundary(path[i-1]) {
			score += 8
		}
		if i == prev+1 {
			score += 4
		}
		if i >= baseStart {
			score += 3
		}
		prev = i
		qi++
	}
	if qi < len(query) {
		return 0, false
	}

	lowerPath := string(path)
	lowerBase := string(path[baseStart:])
	lowerQuery := string(query)
	switch {
	case strings.HasPrefix(lowerBase, lowerQuery):
		score += 30
	case strings.HasPrefix(lowerPath, lowerQuery):
		score += 18
	}
	// Prefer a shallow, short path when otherwise equal.
	score -= len(path) / 20
	if e.Dir {
		score--
	}
	return score, true
}

// isPathBoundary marks the positions where a new path segment or word starts.
func isPathBoundary(r rune) bool {
	switch r {
	case '/', '\\', '_', '-', '.', ' ':
		return true
	}
	return unicode.IsSpace(r)
}

// loadFileIndexCmd builds the index off the Bubble Tea update loop.
func loadFileIndexCmd(root string) tea.Cmd {
	return func() tea.Msg {
		entries, err := collectFileEntries(root)
		return fileIndexMsg{root: root, entries: entries, err: err}
	}
}

// collectFileEntries indexes root, preferring ripgrep for its ignore handling
// and falling back to a bounded Go walk when rg is missing or fails.
func collectFileEntries(root string) ([]fileEntry, error) {
	if root == "" {
		return nil, fmt.Errorf("no working directory")
	}
	if entries, ok := rgFileEntries(root); ok {
		return entries, nil
	}
	return walkFileEntries(root)
}

// rgFileEntries runs `rg --files` with NUL separators. The ok result is false
// for a missing binary, a timeout, or any non-zero exit, in which case the
// caller uses the Go walker.
func rgFileEntries(root string) ([]fileEntry, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), fileIndexTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rg",
		"--files",
		"--hidden",
		"--null",
		"--glob=!.git",
		"--glob=!.git/**",
		"--glob=!node_modules",
		"--glob=!node_modules/**",
		"--glob=!.build",
		"--glob=!.build/**",
		"--glob=!target/**",
		"--glob=!dist/**",
	)
	cmd.Dir = root
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	raw := bytes.Split(out.Bytes(), []byte{0})
	set := make(map[string]fileEntry, len(raw))
	for _, b := range raw {
		if len(b) == 0 {
			continue
		}
		p, ok := cleanIndexedPath(root, string(b))
		if !ok {
			continue
		}
		set[p] = fileEntry{Path: p}
		if len(set) >= fileIndexMaxEntries {
			break
		}
	}
	return entriesFromSet(set), true
}

// walkFileEntries is the fallback indexer. It skips VCS/build directories that
// dominate a walk (and that rg would normally have ignored) and caps the total
// so a mistaken `@` cannot walk forever.
func walkFileEntries(root string) ([]fileEntry, error) {
	set := make(map[string]fileEntry, 1024)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Permission-denied subtrees should not abort the whole index.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if d.IsDir() {
			if skipIndexDir(name) {
				return fs.SkipDir
			}
			set[rel] = fileEntry{Path: rel, Dir: true}
			return nil
		}
		if skipIndexFile(name) {
			return nil
		}
		set[rel] = fileEntry{Path: rel}
		if len(set) >= fileIndexMaxEntries {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entriesFromSet(set), nil
}

// entriesFromSet turns the path set into a deterministic slice, adding parent
// directories so the picker can offer a directory as a selection.
func entriesFromSet(set map[string]fileEntry) []fileEntry {
	for p := range set {
		if set[p].Dir {
			continue
		}
		for d := indexPathDir(p); d != ""; d = indexPathDir(d) {
			if _, ok := set[d]; ok {
				continue
			}
			if skipIndexDir(indexBase(d)) {
				continue
			}
			set[d] = fileEntry{Path: d, Dir: true}
		}
	}
	out := make([]fileEntry, 0, len(set))
	for _, e := range set {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return !out[i].Dir && out[j].Dir
	})
	if len(out) > fileIndexMaxEntries {
		out = out[:fileIndexMaxEntries]
	}
	return out
}

// cleanIndexedPath normalizes one rg line into a relative slash path and
// rejects absolute or escaping paths (rg should only print relative ones).
func cleanIndexedPath(root, p string) (string, bool) {
	if p == "" || strings.HasPrefix(p, "../") || p == ".." ||
		strings.ContainsAny(p, "\n\r") {
		return "", false
	}
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		p = rel
	}
	p = filepath.ToSlash(filepath.Clean(p))
	if p == "." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// indexPathDir returns the parent of a slash path, or "" at the top.
func indexPathDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return ""
	}
	return p[:i]
}

// indexBase returns the final segment of a slash path.
func indexBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// skipIndexDir names the directories that never belong in a quick file picker:
// VCS metadata and build/dependency output. The rg path already honors
// .gitignore; this list is the fallback walker's safety net.
func skipIndexDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".build", "node_modules", "target", "dist",
		"__pycache__", ".venv", "venv", ".next", ".turbo", ".cache", "DerivedData":
		return true
	}
	return false
}

// skipIndexFile filters OS metadata noise from the fallback walker.
func skipIndexFile(name string) bool {
	switch name {
	case ".DS_Store":
		return true
	}
	return false
}

// fileSearchRoot is the directory the @ picker indexes. It is the same launch
// directory the agent's file tools are rooted at — not a resumed session's
// historical header.Cwd, which may name a directory this process never chdir'd
// into and where a relative path from the picker would not resolve.
func (m Model) fileSearchRoot() string {
	if m.session != nil && m.session.cwd != "" {
		return m.session.cwd
	}
	if m.cwd != "" {
		return m.cwd
	}
	return "."
}

// refreshFileMenu recomputes the popup from the editor's current text and caret.
// It returns the async index-load command when the first non-empty @query is
// typed. Pickers own the composer when open, so @ is not allowed to steal that
// mode.
func (m *Model) refreshFileMenu() tea.Cmd {
	if m.menu.picking() {
		m.fileMenu.close()
		return nil
	}
	if m.fileMenu.refresh(m.input.Value(), m.input.CursorByte(), m.fileSearchRoot()) {
		return loadFileIndexCmd(m.fileMenu.root)
	}
	return nil
}

// handleFileIndex folds a completed index build back into the popup. A stale
// result (the session's root changed while it was running) is ignored.
func (m Model) handleFileIndex(msg fileIndexMsg) (tea.Model, tea.Cmd) {
	if msg.root != m.fileMenu.root {
		return m, nil
	}
	m.fileMenu.setIndex(msg.entries, msg.err)
	cmd := m.refreshFileMenu()
	m.relayout()
	return m, cmd
}

// insertFileSelection replaces the active @token with the highlighted path and
// leaves the caret ready for the next word. Inserting a path (rather than
// keeping the @sigil) matches codex's local file picker: the model receives a
// plain path in the prompt.
func (m Model) insertFileSelection() (Model, bool) {
	text, ok := m.fileMenu.selection()
	if !ok {
		return m, false
	}
	value := m.input.Value()
	start, end := m.fileMenu.tokenStart, m.fileMenu.tokenEnd
	if start < 0 || end < start || end > len(value) || !strings.HasPrefix(value[start:end], "@") {
		m.fileMenu.close()
		return m, false
	}
	before, after := value[:start], value[end:]
	insert := text
	caret := len(before) + len(insert)
	switch {
	case after == "":
		insert += " "
		caret++
	case isFileSeparator(after[0]):
		// There is already a separator after the token. Move past it so the
		// next typed rune starts a new word instead of extending the path.
		n := 1
		for n < len(after) && isFileSeparator(after[n]) {
			n++
		}
		caret += n
		if n < len(after) && after[n] != '\n' && after[n] != '\r' {
			// Text follows the separator on the same line: add one more space
			// so typing before that suffix cannot merge into the path.
			insert += " "
			caret = len(before) + len(insert)
		}
	default:
		insert += " "
		caret++
	}
	m.input.SetValueCursor(before+insert+after, caret)
	m.fileMenu.close()
	m.refreshFileMenu()
	m.relayout()
	return m, true
}

// isFileSeparator reports the single-line separators the completer keeps
// between a path and following prompt text.
func isFileSeparator(b byte) bool {
	return b == ' ' || b == '\t'
}
