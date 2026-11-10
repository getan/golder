package mygoui

import (
	"math"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	transcriptRowEstimate = 72.0
	transcriptRowGap      = 14.0
	transcriptPadding     = 18.0
	transcriptOverscan    = 3
)

// transcriptWindow keeps normal rendering virtual, but retains the contiguous
// rows visited by an active text selection. A regular Scroll can then retain
// MyGo's selection endpoints and copy every built paragraph across viewports.
type transcriptWindow struct {
	scroll    ui.ScrollState
	keys      []string
	index     map[string]int
	height    map[string]float64
	measuring map[string]bool
	values    []float64
	tree      []float64

	rowsHead    *transcriptRow
	rowsLen     int
	waiting     bool
	started     bool
	follow      bool
	remeasure   bool
	first       int
	last        int
	builtLo     int
	builtHi     int
	pinned      bool
	pinLo       string
	pinHi       string
	focusKey    string
	selection   transcriptSelectionKey
	ranges      []transcriptRange
	startMarker uint64
	endMarker   uint64
	menuHits    []transcriptMenuHit
	menuSource  string
	menuValid   bool
	reveal      int
	hasReveal   bool
}

type transcriptSelectionKey struct {
	first, last uint64
	start, end  int
	count       int
}

type transcriptRange struct{ lo, hi int }

type transcriptMenuHit struct {
	box       ui.Rect
	source    string
	assistant bool
}

func (s *transcriptWindow) markdownAt(x, y float32) (string, bool) {
	for i := len(s.menuHits) - 1; i >= 0; i-- {
		hit := s.menuHits[i]
		if hit.box.W > 0 && hit.box.H > 0 && x >= hit.box.X && x < hit.box.X+hit.box.W && y >= hit.box.Y && y < hit.box.Y+hit.box.H {
			return hit.source, hit.assistant
		}
	}
	return "", false
}

func transcriptSelection(parts []ui.SelectedText) transcriptSelectionKey {
	if len(parts) == 0 {
		return transcriptSelectionKey{}
	}
	return transcriptSelectionKey{first: parts[0].ID, last: parts[len(parts)-1].ID,
		start: parts[0].Start, end: parts[len(parts)-1].End, count: len(parts)}
}

// A copy can arrive before the frame that materializes newly selected rows.
// Refuse rather than silently copy only the visible ends of the selection.
func (s *transcriptWindow) copyReady(parts []ui.SelectedText) bool {
	if len(parts) == 0 {
		return true
	}
	selectAll := s.startMarker != 0 && s.endMarker != 0 && parts[0].ID == s.startMarker && parts[len(parts)-1].ID == s.endMarker
	if selectAll && (len(s.ranges) != 1 || s.ranges[0].lo != 0 || s.ranges[0].hi != len(s.keys)) {
		return false
	}
	return len(s.ranges) <= 1 || transcriptSelection(parts) == s.selection
}

// copyText omits the zero-size select-all sentinels and never places an opaque
// inline-object replacement character on the clipboard without explanation.
func (s *transcriptWindow) copyText(parts []ui.SelectedText) (string, bool) {
	var paragraphs []string
	objects := false
	for _, part := range parts {
		if part.ID == s.startMarker || part.ID == s.endMarker {
			continue
		}
		runes := []rune(part.Text)
		start := min(max(part.Start, 0), len(runes))
		end := min(max(part.End, start), len(runes))
		text := string(runes[start:end])
		if strings.ContainsRune(text, '\ufffc') {
			objects = true
			text = strings.ReplaceAll(text, "\ufffc", "[formula]")
		}
		paragraphs = append(paragraphs, text)
	}
	return strings.Join(paragraphs, "\n"), objects
}

func (s *transcriptWindow) Visible() (int, int) { return s.first, s.last }

func (s *transcriptWindow) ScrollIntoView(index int) {
	s.reveal, s.hasReveal = index, true
}

func (s *transcriptWindow) sync(rows []transcriptRow, waiting bool) {
	var head *transcriptRow
	if len(rows) > 0 {
		head = &rows[0]
	}
	if s.index != nil && s.rowsHead == head && s.rowsLen == len(rows) && s.waiting == waiting {
		return
	}
	s.remeasure = true
	var anchor string
	var offset float64
	if s.started && s.first >= 0 && s.first < len(s.keys) {
		anchor = s.keys[s.first]
		offset = float64(s.scroll.Y) - s.prefix(s.first)
	}
	if s.height == nil {
		s.height = make(map[string]float64)
		s.measuring = make(map[string]bool)
	}
	s.rowsHead, s.rowsLen, s.waiting = head, len(rows), waiting
	s.keys = make([]string, 0, len(rows)+1)
	for _, row := range rows {
		s.keys = append(s.keys, row.key)
	}
	if waiting {
		s.keys = append(s.keys, "awaiting-first-output")
	}
	s.index = make(map[string]int, len(s.keys))
	s.values = make([]float64, len(s.keys))
	s.tree = make([]float64, len(s.keys)+1)
	for i, key := range s.keys {
		s.index[key] = i
		h := s.height[key]
		if h <= 0 {
			h = transcriptRowEstimate
		}
		s.values[i] = h + transcriptRowGap
		j := i + 1
		s.tree[j] += s.values[i]
		if parent := j + (j & -j); parent < len(s.tree) {
			s.tree[parent] += s.tree[j]
		}
	}
	for key := range s.height {
		if _, ok := s.index[key]; !ok {
			delete(s.height, key)
		}
	}
	for key := range s.measuring {
		if _, ok := s.index[key]; !ok {
			delete(s.measuring, key)
		}
	}
	if anchor != "" && !s.follow {
		if i, ok := s.index[anchor]; ok {
			s.scroll.Y = float32(max(s.prefix(i)+offset, 0))
		}
	}
	if s.pinned {
		_, lo := s.index[s.pinLo]
		_, hi := s.index[s.pinHi]
		if !lo || !hi {
			s.pinned = false
		}
	}
}

// prefix is the total estimated height of the first n rows and their gaps.
func (s *transcriptWindow) prefix(n int) float64 {
	n = min(max(n, 0), len(s.values))
	var total float64
	for n > 0 {
		total += s.tree[n]
		n -= n & -n
	}
	return total
}

func (s *transcriptWindow) rowAt(y float64) int {
	if len(s.values) == 0 {
		return 0
	}
	i, sum := 0, 0.0
	step := 1
	for step < len(s.tree) {
		step <<= 1
	}
	for step >>= 1; step > 0; step >>= 1 {
		j := i + step
		if j < len(s.tree) && sum+s.tree[j] <= y {
			i, sum = j, sum+s.tree[j]
		}
	}
	return min(i, len(s.values)-1)
}

func (s *transcriptWindow) measure(i int, h float32) bool {
	if i < 0 || i >= len(s.keys) || h <= 0 {
		return false
	}
	value := float64(h) + transcriptRowGap
	delta := value - s.values[i]
	if math.Abs(delta) < 0.5 {
		s.height[s.keys[i]] = float64(h)
		return false
	}
	known := s.height[s.keys[i]] > 0
	s.values[i], s.height[s.keys[i]] = value, float64(h)
	for j := i + 1; j < len(s.tree); j += j & -j {
		s.tree[j] += delta
	}
	// The first measurement replaces an estimate for a row already laid out
	// at its real height. Correcting the scroll offset for that difference
	// applies the change twice and moves the text under an active drag.
	if known && s.started && !s.follow && i < s.first {
		s.scroll.Y = max(s.scroll.Y+float32(delta), 0)
	}
	return true
}

func (s *transcriptWindow) plan(viewport float32, parts []ui.SelectedText) []transcriptRange {
	n := len(s.keys)
	if n == 0 {
		s.first, s.last, s.builtLo, s.builtHi = 0, -1, 0, 0
		return nil
	}
	if viewport <= 0 {
		viewport = 600
	}
	if !s.started {
		s.follow, s.started = true, true
	} else if s.scroll.MaxY > 0 {
		s.follow = s.scroll.Y >= s.scroll.MaxY-1
	}
	selection := transcriptSelection(parts)
	changed := selection != s.selection
	if len(parts) > 0 {
		s.follow = false
		if s.startMarker != 0 && s.endMarker != 0 && parts[0].ID == s.startMarker && parts[len(parts)-1].ID == s.endMarker {
			// The native Select All selected both invisible boundary markers.
			// Materialize all rows before Copy so it cannot omit virtual rows.
			s.pinned, s.pinLo, s.pinHi = true, s.keys[0], s.keys[n-1]
		} else if !s.pinned && s.builtHi > s.builtLo && s.builtHi <= n {
			s.pinned = true
			s.pinLo, s.pinHi = s.keys[s.builtLo], s.keys[s.builtHi-1]
		}
	} else {
		s.pinned = false
	}
	s.selection = selection
	if s.hasReveal {
		i := min(max(s.reveal, 0), n-1)
		s.scroll.Y = float32(max(s.prefix(i), 0))
		s.follow, s.hasReveal = false, false
	}
	y := float64(s.scroll.Y)
	if s.follow {
		y = max(s.prefix(n)-float64(viewport)+2*transcriptPadding, 0)
		s.scroll.Y = math.MaxFloat32
	}
	s.first = s.rowAt(max(y-transcriptPadding, 0))
	s.last = s.rowAt(max(y+float64(viewport)-transcriptPadding, 0))
	lo := max(s.first-transcriptOverscan, 0)
	hi := min(s.last+transcriptOverscan+1, n)
	s.builtLo, s.builtHi = lo, hi
	ranges := []transcriptRange{{lo, hi}}
	if s.pinned {
		pinLo, okLo := s.index[s.pinLo]
		pinHi, okHi := s.index[s.pinHi]
		if !okLo || !okHi {
			s.pinned = false
		} else {
			pinHi++
			if changed {
				pinLo, pinHi = min(pinLo, lo), max(pinHi, hi)
				s.pinLo, s.pinHi = s.keys[pinLo], s.keys[pinHi-1]
			}
			ranges = append(ranges, transcriptRange{pinLo, pinHi})
		}
	}
	if i, ok := s.index[s.focusKey]; ok {
		ranges = append(ranges, transcriptRange{i, i + 1})
	}
	slices.SortFunc(ranges, func(a, b transcriptRange) int { return a.lo - b.lo })
	merged := ranges[:0]
	for _, r := range ranges {
		if n := len(merged); n > 0 && r.lo <= merged[n-1].hi {
			merged[n-1].hi = max(merged[n-1].hi, r.hi)
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}
