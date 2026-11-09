// Package explore describes the read-only operations folded into an Explore
// transcript cell. It is presentation policy, not an authorization decision.
package explore

import "strings"

// IsTool names the same deliberately narrow set in both GUI and TUI. Shell,
// network, and result-bearing tools retain their own cards.
func IsTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "ls", "grep", "find", "view_image":
		return true
	default:
		return false
	}
}

// Step summarizes one native exploration tool without guessing from its output.
func Step(name string, input map[string]any) (verb, detail string) {
	arg := func(key string) string {
		value, _ := input[key].(string)
		return strings.Join(strings.Fields(value), " ")
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read":
		return "Read", arg("path")
	case "ls":
		path := arg("path")
		if path == "" {
			path = "."
		}
		return "List", path
	case "grep":
		pattern := arg("pattern")
		if pattern == "" {
			return "Search", ""
		}
		if scope := arg("path"); scope != "" {
			return "Search", pattern + " in " + scope
		}
		if glob := arg("glob"); glob != "" {
			return "Search", pattern + " in " + glob
		}
		return "Search", pattern
	case "find":
		glob := arg("glob")
		if glob == "" {
			return "Find", ""
		}
		if scope := arg("path"); scope != "" {
			return "Find", glob + " in " + scope
		}
		return "Find", glob
	case "view_image":
		return "View", arg("path")
	default:
		return "", ""
	}
}

// Row carries the execution envelope of the calls it summarizes, so mixed
// sandbox/unconfined groups never give one misleading group-wide label.
type Row struct {
	Text     string
	Envelope string
}

// Builder merges adjacent reads, retaining their original order and envelope.
type Builder struct {
	rows     []Row
	reads    []string
	readEnv  string
	seenRead map[string]bool
}

func (b *Builder) flushReads() {
	if len(b.reads) == 0 {
		return
	}
	b.rows = append(b.rows, Row{Text: "Read " + strings.Join(b.reads, ", "), Envelope: b.readEnv})
	b.reads, b.readEnv, b.seenRead = nil, "", nil
}

// Add appends one tool call in transcript order.
func (b *Builder) Add(name string, input map[string]any, envelope string) {
	verb, detail := Step(name, input)
	if verb == "" {
		return
	}
	if verb == "Read" {
		if len(b.reads) > 0 && envelope != "" && b.readEnv != "" && envelope != b.readEnv {
			b.flushReads()
		}
		if b.seenRead == nil {
			b.seenRead = make(map[string]bool)
		}
		if !b.seenRead[detail] {
			b.seenRead[detail] = true
			b.reads = append(b.reads, detail)
		}
		if b.readEnv == "" {
			b.readEnv = envelope
		}
		return
	}
	b.flushReads()
	text := verb
	if detail != "" {
		text += " " + detail
	}
	b.rows = append(b.rows, Row{Text: text, Envelope: envelope})
}

// Finish returns the summary accumulated so far.
func (b *Builder) Finish() []Row {
	b.flushReads()
	return b.rows
}
