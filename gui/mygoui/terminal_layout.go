package mygoui

// terminalLayout is a binary split tree. Leaves own a pane; branch ratios are
// percentages so resizing the enclosing window preserves their proportions.
type terminalLayout struct {
	pane     *terminalPane
	first    *terminalLayout
	second   *terminalLayout
	vertical bool
	percent  float32
}

func (n *terminalLayout) split(id string, pane *terminalPane, vertical bool) bool {
	if n == nil || pane == nil {
		return false
	}
	if n.pane != nil {
		if n.pane.id != id {
			return false
		}
		old := *n
		*n = terminalLayout{first: &old, second: &terminalLayout{pane: pane}, vertical: vertical, percent: 50}
		return true
	}
	return n.first.split(id, pane, vertical) || n.second.split(id, pane, vertical)
}

func (n *terminalLayout) remove(id string) (*terminalLayout, *terminalPane) {
	if n == nil {
		return nil, nil
	}
	if n.pane != nil {
		if n.pane.id == id {
			return nil, n.pane
		}
		return n, nil
	}
	var removed *terminalPane
	n.first, removed = n.first.remove(id)
	if removed == nil {
		n.second, removed = n.second.remove(id)
	}
	if n.first == nil {
		return n.second, removed
	}
	if n.second == nil {
		return n.first, removed
	}
	return n, removed
}

func (n *terminalLayout) each(fn func(*terminalPane)) {
	if n == nil {
		return
	}
	if n.pane != nil {
		fn(n.pane)
		return
	}
	n.first.each(fn)
	n.second.each(fn)
}

// resizePane sets the share of the closest split directly containing a leaf.
func (n *terminalLayout) resizePane(id string, percent float32) bool {
	if n == nil || n.pane != nil {
		return false
	}
	if n.first != nil && n.first.pane != nil && n.first.pane.id == id {
		n.percent = percent
		return true
	}
	if n.second != nil && n.second.pane != nil && n.second.pane.id == id {
		n.percent = 100 - percent
		return true
	}
	return n.first.resizePane(id, percent) || n.second.resizePane(id, percent)
}
