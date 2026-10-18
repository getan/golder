package judge

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/getan/golder/internal/grants"
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
		fmt.Fprintf(out, "  [y] allow reads and writes to %s (this session)   [s] remember it   [N] skip\n", path)
	} else {
		fmt.Fprintf(out, "  [y] allow reading %s (this session)   [s] remember it   [N] skip\n", path)
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
	default:
		return ApprovalAnswer{Answered: true}
	}
	ans := ApprovalAnswer{Approve: true, Answered: true, Persisted: lifeRemember}
	if writing {
		life := grants.Session
		if lifeRemember {
			life = grants.Remembered
		}
		// Unlike the TUI write rows, this path has no driver-side registration
		// step. Register here so a "yes" in the REPL actually widens the
		// sandbox the retry runs in.
		if _, err := grants.Add(grants.Grant{
			Path: path, Allow: grants.ReadWrite, Scope: grants.Subtree,
			Lifetime: life, Source: "sandbox re-admission",
		}); err != nil {
			return ApprovalAnswer{Answered: true}
		}
		ans.WritableRoot = path
	} else {
		ans.ReadRoot = path
	}
	return ans
}
