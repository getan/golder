package redact

// ahoNode is one state of the anchor automaton. next is keyed by raw byte;
// buildAho installs both cases for ASCII letters, so the scan runs directly on
// the original text without a lowercased copy.
type ahoNode struct {
	next map[byte]int
	fail int
	out  []int // rule indices
}

type automaton struct {
	nodes []ahoNode
}

func buildAho(rules []*rule) *automaton {
	a := &automaton{nodes: []ahoNode{{next: map[byte]int{}}}}
	for ri, r := range rules {
		for _, anchor := range r.anchors {
			state := 0
			for i := 0; i < len(anchor); i++ {
				variants := caseBytes(anchor[i])
				next := -1
				for _, b := range variants {
					if n, ok := a.nodes[state].next[b]; ok {
						next = n
						break
					}
				}
				if next < 0 {
					next = len(a.nodes)
					a.nodes = append(a.nodes, ahoNode{next: map[byte]int{}})
				}
				for _, b := range variants {
					a.nodes[state].next[b] = next
				}
				state = next
			}
			a.nodes[state].out = appendUniqueRule(a.nodes[state].out, ri)
		}
	}

	// Breadth-first fail links, the standard Aho-Corasick construction.
	//
	// A letter's two case edges point at the same node, so the node map holds
	// the same child twice. Processing it twice enqueued every descendant twice
	// and appended the failure outputs again on each visit; one node's output
	// list grew to thousands of copies of the same rule. seen makes a node's
	// fail link and output merge happen once, whatever edge reached it.
	seen := make([]bool, len(a.nodes))
	var queue []int
	for _, n := range a.nodes[0].next {
		if seen[n] {
			continue
		}
		seen[n] = true
		a.nodes[n].fail = 0
		queue = append(queue, n)
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for b, n := range a.nodes[s].next {
			if seen[n] {
				continue
			}
			f := a.nodes[s].fail
			for f != 0 {
				if _, ok := a.nodes[f].next[b]; ok {
					break
				}
				f = a.nodes[f].fail
			}
			if next, ok := a.nodes[f].next[b]; ok && next != n {
				a.nodes[n].fail = next
			} else {
				a.nodes[n].fail = 0
			}
			for _, ri := range a.nodes[a.nodes[n].fail].out {
				a.nodes[n].out = appendUniqueRule(a.nodes[n].out, ri)
			}
			seen[n] = true
			queue = append(queue, n)
		}
	}
	return a
}

// appendUniqueRule adds ri only when the list does not already hold it. Rule
// order is kept: the anchors' own rules come first, in table order, and the
// failure links add what they contribute in BFS order.
func appendUniqueRule(out []int, ri int) []int {
	for _, have := range out {
		if have == ri {
			return out
		}
	}
	return append(out, ri)
}

// step advances one byte. A missing transition falls back through fail links.
func (a *automaton) step(state int, b byte) int {
	for {
		if next, ok := a.nodes[state].next[b]; ok {
			return next
		}
		if state == 0 {
			return 0
		}
		state = a.nodes[state].fail
	}
}

func caseBytes(b byte) []byte {
	switch {
	case b >= 'a' && b <= 'z':
		return []byte{b, b - 32}
	case b >= 'A' && b <= 'Z':
		return []byte{b, b + 32}
	default:
		return []byte{b}
	}
}
