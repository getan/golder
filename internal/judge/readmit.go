package judge

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

// AskReAdmit is the stdin shape of the sandbox re-admission question. It is the
// same three answers the TUI's re-admit dialog offers, in one block: allow the
// path for this session (contained), remember it, or skip. It exists so a REPL
// user can unblock a command the sandbox just refused instead of being told to
// run golder in another front-end.
func AskReAdmit(ctx context.Context, in *bufio.Reader, out io.Writer, mu *sync.Mutex, path, summary, lang string) ApprovalAnswer {
	if in == nil || out == nil {
		return ApprovalAnswer{}
	}
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	writing := PathWriteNeed(path, summary)
	verb := "read"
	if writing {
		verb = "write"
	}
	fmt.Fprintf(out, "\nthe sandbox denied %s for this %s.\n", path, verb)
	if writing {
		fmt.Fprintf(out, "  [y] allow reads and writes to %s (this session)   [s] remember it"+
			"   [e] request one unconfined run (the gate decides)   [N] skip\n", path)
	} else {
		fmt.Fprintf(out, "  [y] allow reading %s (this session)   [s] remember it"+
			"   [e] request one unconfined run (the gate decides)   [N] skip\n", path)
	}
	fmt.Fprint(out, "Choice: ")
	line, ok := readLineOrCancel(ctx, in)
	if !ok {
		return ApprovalAnswer{}
	}
	lifeRemember := false
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
	case "s", "save":
		lifeRemember = true
	case "e", "escape", "outside":
		// Run this one command without the sandbox. The command already ran and
		// was already reviewed; what the sandbox refused is a path, so this is a
		// decision about this call only, and nothing is remembered.
		return ApprovalAnswer{Approve: true, Unconfined: true, Answered: true}
	default:
		return ApprovalAnswer{Answered: true}
	}
	// The answer states the intent (which root, which lifetime); registering it
	// belongs to the gate assembly, which owns the authorization table and the
	// sandbox's writable roots — the same owner the TUI's rows go through. A
	// prompt that registered here would be a second writer, and its failure
	// would have to invent a refusal of its own.
	ans := ApprovalAnswer{Approve: true, Answered: true, Persisted: lifeRemember}
	if writing {
		ans.WritableRoot = path
	} else {
		ans.ReadRoot = path
	}
	return ans
}
