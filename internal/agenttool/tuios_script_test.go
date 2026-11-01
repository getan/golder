package agenttool

// Failure model: a missing/short/reordered/duplicated frame, lost terminator,
// wrong request id, or damaged payload must not execute even a script prefix.
// Frames landing in a shell after ssh exits must be inert. Echo of the setup
// line must not look like a receipt. Test the actual POSIX receiver, not a mock
// checksum implementation; no remote daemon, Python or root is involved.
import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTuiosScriptReceiverIntegrity(t *testing.T) {
	for _, mutation := range []string{"intact", "short", "corrupt", "missing", "duplicate", "reorder", "wrong-id", "no-commit"} {
		t.Run(mutation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "effect")
			body := "printf '中文-ok' > " + quoteControlArg(path) + "\n#" + strings.Repeat("padding", 2000) + "\nexit 7\n"
			plan, err := newTuiosScript(body)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.bootstrap) > 3072 {
				t.Fatalf("setup line too long: %d", len(plan.bootstrap))
			}
			frames := strings.Split(strings.TrimSuffix(plan.frames, "\n"), "\n")
			for _, line := range frames {
				if len(line) > 768 || !strings.HasPrefix(line, "#") {
					t.Fatal("unsafe wire line")
				}
			}
			if plan.receipt.MatchString(plan.bootstrap) {
				t.Fatal("command echo can forge receipt")
			}
			switch mutation {
			case "short":
				frames[1] = frames[1][:len(frames[1])-3]
			case "corrupt":
				last := len(frames[1]) - 1
				replacement := "A"
				if frames[1][last] == 'A' {
					replacement = "B"
				}
				frames[1] = frames[1][:last] + replacement
			case "missing":
				frames = append(frames[:1], frames[2:]...)
			case "duplicate":
				frames = append(frames[:1], append([]string{frames[0]}, frames[1:]...)...)
			case "reorder":
				frames[0], frames[1] = frames[1], frames[0]
			case "wrong-id":
				frames[0] = "#wrong:0:AAAA"
			case "no-commit":
				frames = frames[:len(frames)-1]
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", plan.receiver)
			cmd.Stdin = strings.NewReader(strings.Join(frames, "\n") + "\n")
			out, runErr := cmd.CombinedOutput()
			data, readErr := os.ReadFile(path)
			if mutation == "intact" {
				if string(data) != "中文-ok" || readErr != nil {
					t.Fatalf("script not executed intact: %s %v %s", data, readErr, out)
				}
				state, code := plan.parseReceipt(string(out))
				if state != "completed" || code != 7 || runErr != nil {
					t.Fatalf("receipt: %s %d %v %s", state, code, runErr, out)
				}
			} else if !os.IsNotExist(readErr) || runErr == nil {
				t.Fatalf("damaged input executed or reported success: %v %v %s", readErr, runErr, out)
			}
			// The exact wire bytes must also be harmless to a parent login shell.
			cmd = exec.CommandContext(ctx, "sh", "-c", strings.Join(frames, "\n"))
			if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
				t.Fatalf("frames are not inert shell comments: %v %s", err, out)
			}
		})
	}
}

func TestTuiosScriptReceiptsSurviveWrapping(t *testing.T) {
	p, err := newTuiosScript("exit 0")
	if err != nil {
		t.Fatal(err)
	}
	wrapped := strings.Join(strings.Split("G:"+p.id+":D003", ""), "\n")
	state, code := p.parseReceipt(wrapped)
	if state != "completed" || code != 3 {
		t.Fatalf("wrapped receipt: %s %d", state, code)
	}
	other, _ := newTuiosScript("exit 0")
	if state, _ := other.parseReceipt(wrapped); state != "" {
		t.Fatal("another pane/request's receipt matched")
	}
}

func TestTuiosScriptSyntaxCheckedBeforeEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist")
	p, err := newTuiosScript("touch " + quoteControlArg(path) + "\nif\n")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", p.receiver)
	cmd.Stdin = strings.NewReader(p.frames)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("invalid script accepted: %s", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("script prefix executed before syntax check")
	}
}
