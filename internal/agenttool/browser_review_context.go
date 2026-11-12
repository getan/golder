package agenttool

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// These are page-supplied observations, never the accessible name of the
// target and never authorization to select a different element.
type browserReviewContext struct {
	Relation   string `json:"relation"`
	Container  string `json:"container_ref,omitempty"`
	Text       string `json:"text"`
	Visibility string `json:"visibility"`
}

const browserReviewContextNodes = 48

var browserCredentialAssignment = regexp.MustCompile(`(?i)\b(?:password|passwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie)\s*[:=]\s*[^\s,;]+`)
var browserCredentialBearer = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/-]{8,}`)
var browserCredentialField = regexp.MustCompile(`(?i)^\s*(?:password|passwd|secret|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|authorization|cookie)\s*:?\s*$`)
var browserLongOpaque = regexp.MustCompile(`\b[A-Za-z0-9_+/=-]{32,}\b`)

// Query parameters and fragments frequently carry credentials. The full URL
// still binds the target privately; the reviewer only needs its origin/path.
func browserSafeReviewURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "[URL unavailable]"
	}
	hadQuery := u.RawQuery != "" || u.Fragment != ""
	u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
	text := browserSafeReviewText(u.String(), 1000)
	if hadQuery {
		text += "?[query or fragment withheld]"
	}
	return text
}

// A CSS locator has no AX ref to correlate to a row. Only expose its own text
// when its bounded markup contains no editable controls or sensitive form.
func browserReviewSafeMarkup(markup string) bool {
	return browserReviewMarkupSafe(markup, false)
}

// A semantic item may contain its checkable input, but never editable fields.
func browserReviewSafeContainerMarkup(markup string) bool {
	return browserReviewMarkupSafe(markup, true)
}

func browserReviewMarkupSafe(markup string, allowCheckable bool) bool {
	if len(markup) == 0 || len(markup) > 8192 {
		return false
	}
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		return false
	}
	checkableInputs := 0
	var safe func(*html.Node) bool
	safe = func(n *html.Node) bool {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "form", "textarea", "select", "option", "script", "style", "iframe":
				return false
			case "input":
				inputType := ""
				for _, a := range n.Attr {
					if strings.EqualFold(a.Key, "type") {
						inputType = strings.ToLower(a.Val)
					}
				}
				if !allowCheckable || (inputType != "checkbox" && inputType != "radio") {
					return false
				}
				checkableInputs++
				if checkableInputs > 1 {
					return false
				}
			}
			for _, a := range n.Attr {
				if strings.EqualFold(a.Key, "contenteditable") && !strings.EqualFold(a.Val, "false") {
					return false
				}
				if strings.EqualFold(a.Key, "role") && browserReviewEditableRole(a.Val) {
					return false
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if !safe(child) {
				return false
			}
		}
		return true
	}
	return safe(doc)
}

// Refuse whole credential-shaped snippets rather than trying to partially
// reveal a secret. This is defense in depth, not a general secret detector.
// Input values are excluded structurally and are never used as context.
func browserSafeReviewText(text string, limit int) string {
	if browserCredentialAssignment.MatchString(text) || browserCredentialBearer.MatchString(text) || browserLongOpaque.MatchString(text) {
		return "[credential-like text withheld]"
	}
	return DisplayBrowserEvidence(text, limit)
}

func browserReviewTextRole(role string) bool {
	switch strings.ToLower(role) {
	case "statictext", "text":
		return true
	}
	return false
}

func browserReviewEditableRole(role string) bool {
	r := strings.ToLower(role)
	return strings.Contains(r, "textbox") || strings.Contains(r, "password") || r == "searchbox" || r == "combobox" || r == "spinbutton"
}

func browserReviewControlRole(role string) bool {
	switch strings.ToLower(role) {
	case "button", "link", "checkbox", "radio", "switch", "tab", "option", "menuitem", "menuitemcheckbox", "menuitemradio", "slider", "treeitem":
		return true
	}
	return browserReviewEditableRole(role)
}

func browserReviewStatefulRole(role string) bool {
	switch strings.ToLower(role) {
	case "checkbox", "radio", "switch", "slider", "menuitemcheckbox", "menuitemradio":
		return true
	}
	return browserReviewEditableRole(role)
}

func browserReviewCheckableRole(role string) bool {
	switch strings.ToLower(role) {
	case "checkbox", "radio", "switch", "menuitemcheckbox", "menuitemradio":
		return true
	}
	return false
}

// AX snapshots are commonly breadth-first, not contiguous subtrees. Only
// explicit host-supplied parent links establish an association.
type browserReviewTree struct {
	nodes    []browserObservedNode
	byRef    map[string]int
	children map[int][]int
}

func newBrowserReviewTree(nodes []browserObservedNode) *browserReviewTree {
	t := &browserReviewTree{nodes: nodes, byRef: make(map[string]int, len(nodes)), children: make(map[int][]int)}
	for i, n := range nodes {
		if n.Ref == "" {
			return nil
		}
		if _, exists := t.byRef[n.Ref]; exists {
			return nil
		}
		t.byRef[n.Ref] = i
	}
	for i, n := range nodes {
		if n.ParentRef == "" {
			continue
		}
		parent, ok := t.byRef[n.ParentRef]
		if !ok || nodes[parent].Depth >= n.Depth {
			return nil // also rules out cycles
		}
		t.children[parent] = append(t.children[parent], i)
	}
	return t
}

func (t *browserReviewTree) ancestors(idx, limit int) []int {
	var out []int
	for len(out) < limit && t.nodes[idx].ParentRef != "" {
		idx = t.byRef[t.nodes[idx].ParentRef]
		out = append(out, idx)
	}
	return out
}

func (t *browserReviewTree) subtree(root int) ([]int, bool) {
	indexes := []int{root}
	for i := 0; i < len(indexes); i++ {
		indexes = append(indexes, t.children[indexes[i]]...)
		if len(indexes) > browserReviewContextNodes {
			return nil, false
		}
	}
	return indexes, true
}

func (t *browserReviewTree) localText(indexes []int, relation string, probe func(string) (*bool, error)) ([]browserReviewContext, []browserReviewNode, bool, error) {
	var proof []browserReviewNode
	for _, i := range indexes {
		if browserReviewEditableRole(t.nodes[i].Role) || (browserReviewTextRole(t.nodes[i].Role) && browserCredentialField.MatchString(t.nodes[i].Name)) {
			for _, j := range indexes {
				proof = append(proof, t.nodes[j].browserReviewNode)
			}
			return nil, proof, false, nil
		}
	}
	var out []browserReviewContext
	for _, i := range indexes[1:] {
		n := &t.nodes[i]
		if !browserReviewTextRole(n.Role) || strings.TrimSpace(n.Name) == "" {
			continue
		}
		text := browserSafeReviewText(n.Name, 160)
		if text == "" || text == "[credential-like text withheld]" {
			continue
		}
		if n.Visible == nil && probe != nil {
			visible, err := probe(n.Ref)
			if err != nil {
				return nil, nil, false, err
			}
			n.Visible = visible
		}
		if n.Visible == nil || !*n.Visible {
			continue
		}
		hidden := false
		for _, ancestor := range t.ancestors(i, len(t.nodes)) {
			if t.nodes[ancestor].Visible != nil && !*t.nodes[ancestor].Visible {
				hidden = true
				break
			}
		}
		if !hidden && len(out) < 6 {
			out = append(out, browserReviewContext{Relation: relation, Container: DisplayBrowserEvidence(t.nodes[indexes[0]].Ref, 80), Text: text, Visibility: "visible"})
		}
	}
	for _, i := range indexes {
		proof = append(proof, t.nodes[i].browserReviewNode)
	}
	return out, proof, true, nil
}

// browserAssociatedContext never searches the page for a matching label and
// cannot resolve or authorize a different control.
func browserAssociatedContext(t *browserReviewTree, idx int, probe func(string) (*bool, error)) ([]browserReviewContext, string, []browserReviewNode, error) {
	if t == nil || idx < 0 || idx >= len(t.nodes) {
		return nil, "target_unavailable", nil, nil
	}
	target := t.nodes[idx]
	if strings.TrimSpace(target.Name) != "" {
		return nil, "target_has_accessible_name", nil, nil
	}
	if browserReviewEditableRole(target.Role) {
		return nil, "unnamed_editable_context_withheld", nil, nil
	}
	if browserReviewCheckableRole(target.Role) {
		_, itemStatus := browserReviewSemanticItemStatus(t, idx)
		if itemStatus == "ambiguous" {
			return nil, "unnamed_target_no_verified_visible_context", nil, nil
		}
		if itemStatus == "too_large" {
			return nil, "unnamed_target_context_too_large", nil, nil
		}
	}
	indexes, complete := t.subtree(idx)
	if !complete {
		return nil, "unnamed_target_context_too_large", nil, nil
	}
	context, proof, safe, err := t.localText(indexes, "target_descendant", probe)
	if err != nil {
		return nil, "", nil, err
	}
	if !safe {
		return nil, "unnamed_target_sensitive_or_editable_context_withheld", proof, nil
	}
	if len(context) > 0 {
		return context, "unnamed_target_visible_descendant_context", proof, nil
	}
	ancestors := t.ancestors(idx, 6)
	// Prefer an explicit row/list item over a nested generic wrapper: the latter
	// may contain a caption but omit the permission scope in its sibling cell.
	for pass := 0; pass < 2; pass++ {
		for _, parent := range ancestors {
			role := strings.ToLower(t.nodes[parent].Role)
			semantic := role == "row" || role == "layouttablerow" || role == "listitem"
			if (pass == 0 && !semantic) || (pass == 1 && role != "generic" && role != "group") {
				continue
			}
			indexes, complete := t.subtree(parent)
			if !complete {
				if semantic && browserReviewCheckableRole(target.Role) {
					return nil, "unnamed_target_context_too_large", proof, nil
				}
				continue
			}
			controls := 0
			for _, i := range indexes {
				counted := browserReviewControlRole(t.nodes[i].Role)
				if semantic && browserReviewCheckableRole(target.Role) {
					counted = browserReviewStatefulRole(t.nodes[i].Role)
				}
				if counted {
					controls++
				}
			}
			if controls != 1 || !browserReviewControlRole(target.Role) {
				if semantic && browserReviewCheckableRole(target.Role) {
					return nil, "unnamed_target_no_verified_visible_context", proof, nil
				}
				continue
			}
			context, p, safe, err := t.localText(indexes, "same_item", probe)
			if err != nil {
				return nil, "", nil, err
			}
			proof = append(proof, p...)
			if !safe {
				return nil, "unnamed_item_sensitive_or_editable_context_withheld", proof, nil
			}
			if len(context) > 0 {
				return context, "unnamed_target_visible_item_context", proof, nil
			}
		}
	}
	return nil, "unnamed_target_no_verified_visible_context", proof, nil
}

// Only a complete, explicit semantic item with one stateful control can be
// probed for rendered text. Generic wrappers are not reliable row boundaries.
func browserReviewSemanticItem(t *browserReviewTree, idx int) string {
	ref, _ := browserReviewSemanticItemStatus(t, idx)
	return ref
}

func browserReviewSemanticItemStatus(t *browserReviewTree, idx int) (string, string) {
	if t == nil || idx < 0 || idx >= len(t.nodes) || !browserReviewCheckableRole(t.nodes[idx].Role) {
		return "", "unavailable"
	}
	for _, parent := range t.ancestors(idx, 6) {
		role := strings.ToLower(t.nodes[parent].Role)
		if role != "row" && role != "layouttablerow" && role != "listitem" {
			continue
		}
		indexes, complete := t.subtree(parent)
		if !complete {
			return "", "too_large"
		}
		controls := 0
		for _, i := range indexes {
			if browserReviewStatefulRole(t.nodes[i].Role) {
				controls++
			}
		}
		if controls == 1 {
			return t.nodes[parent].Ref, "unique"
		}
		return "", "ambiguous"
	}
	return "", "unavailable"
}

func browserReviewItemHasAuxiliaryControls(t *browserReviewTree, ref string) bool {
	root, ok := t.byRef[ref]
	if !ok {
		return true
	}
	indexes, complete := t.subtree(root)
	if !complete {
		return true
	}
	for _, i := range indexes {
		role := t.nodes[i].Role
		if browserReviewControlRole(role) && !browserReviewStatefulRole(role) {
			return true
		}
	}
	return false
}
