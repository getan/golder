package runtime

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

const (
	gitBriefTimeout  = time.Second
	gitBriefMaxLines = 40
	gitBriefMaxBytes = 8 << 10
)

// gitStatusBrief is optional startup context, never a reason to delay or block
// cancellation of a task. Limit time, pipe-drain time, lines AND bytes while
// reading, rather than collecting the entire git output and trimming later.
func gitStatusBrief(ctx context.Context, dir string) string {
	return gitStatusBriefWithTimeout(ctx, dir, gitBriefTimeout)
}

func gitStatusBriefWithTimeout(ctx context.Context, dir string, timeout time.Duration) string {
	dir = strings.TrimSpace(dir)
	if dir == "" || ctx.Err() != nil {
		return ""
	}
	probe, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(probe, "git", "--no-optional-locks", "status", "--porcelain", "-b")
	cmd.Dir = dir
	// Git can start helpers holding its stdout pipe open. Bound the pipe wait
	// too; killing only git must not leave task cancellation waiting on a helper.
	cmd.WaitDelay = 100 * time.Millisecond
	out := &gitBriefOutput{cancel: cancel}
	cmd.Stdout = out
	err := cmd.Run()
	if ctx.Err() != nil {
		return ""
	}
	if out.truncated {
		return strings.TrimRight(out.buf.String(), "\n") + "\n… (truncated)"
	}
	if err != nil {
		return ""
	}
	return strings.TrimRight(out.buf.String(), "\n")
}

// gitBriefOutput bounds retained bytes even for a single enormous filename.
// Reaching either cap cancels git so a large repository is not scanned just
// to discard the output. os/exec has one stdout-copy goroutine and Run joins
// it before the buffer is read, so no lock is needed here.
type gitBriefOutput struct {
	buf       bytes.Buffer
	lines     int
	truncated bool
	cancel    context.CancelFunc
}

func (w *gitBriefOutput) Write(p []byte) (int, error) {
	if w.truncated {
		return len(p), nil
	}
	for _, b := range p {
		w.buf.WriteByte(b)
		if b == '\n' {
			w.lines++
		}
		if w.buf.Len() >= gitBriefMaxBytes || w.lines >= gitBriefMaxLines {
			w.truncated = true
			w.cancel()
			break
		}
	}
	return len(p), nil
}
