package finalizemac

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type staplerFixture struct {
	bin, log, count, dir string
}

func newStaplerFixture(t *testing.T) staplerFixture {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	f := staplerFixture{dir: t.TempDir()}
	f.bin, f.log, f.count = filepath.Join(f.dir, "bin"), filepath.Join(f.dir, "calls"), filepath.Join(f.dir, "count")
	if err := os.Mkdir(f.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"xcrun": `printf 'xcrun %s\n' "$*" >> "$TEST_LOG"
count=0
if [[ -f "$TEST_COUNT" ]]; then count=$(cat "$TEST_COUNT"); fi
count=$((count + 1))
printf '%s' "$count" > "$TEST_COUNT"
if [[ "$count" -le "$TEST_FAILURES" ]]; then
  printf '%s\n' "$TEST_ERROR" >&2
  exit 68
fi
echo 'The action worked!'`,
		"sleep": `printf 'sleep %s\n' "$*" >> "$TEST_LOG"`,
		"codesign": `printf 'codesign %s\n' "$*" >> "$TEST_LOG"
if [[ "$TEST_SIGNATURE" == invalid ]]; then exit 1; fi
if [[ "$1" == -dvv ]]; then
  if [[ "$TEST_SIGNATURE" == wrong-team ]]; then
    echo 'TeamIdentifier=OTHERTEAM'
  else
    echo 'TeamIdentifier=EXPECTEDTEAM'
  fi
  if [[ "$TEST_SIGNATURE" != ad-hoc ]]; then
    echo 'Authority=Developer ID Application: Example'
  fi
fi`,
	} {
		if err := os.WriteFile(filepath.Join(f.bin, name), []byte("#!/bin/bash\nset -eu\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f staplerFixture) run(t *testing.T, failures int, message, signature string, args ...string) (string, int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", args...)
	cmd.Env = append(os.Environ(), "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TEST_LOG="+f.log, "TEST_COUNT="+f.count, "TEST_FAILURES="+strconv.Itoa(failures),
		"TEST_ERROR="+message, "TEST_SIGNATURE="+signature)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("mocked signing verification timed out: %s", out)
	}
	status := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			status = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	log, _ := os.ReadFile(f.log)
	return string(out), status, string(log)
}

func TestStaplerRetriesOnlyDiagnosedTransientErrors(t *testing.T) {
	for _, action := range []string{"staple", "validate"} {
		for _, tc := range []struct {
			name, message string
			failures      int
			wantStatus    int
			wantAttempts  int
		}{
			{"success", "", 0, 0, 1},
			{"CI CloudKit connection loss", `error is Error Domain=NSURLErrorDomain Code=-1005 "The network connection was lost."` + "\nThe validate action failed! Error 68.", 2, 0, 3},
			{"timeout", "NSURLErrorDomain Code=-1001 (timed out)", 1, 0, 2},
			{"HTTP unavailable", "HTTP status code: 503", 1, 0, 2},
			{"HTTP rate limited", "HTTP 429", 1, 0, 2},
			{"exhausted", "NSURLErrorDomain Code=-1005 (lost)", 10, 68, 4},
			{"missing ticket", "Could not find a ticket. The validate action failed! Error 68.", 10, 68, 1},
			{"invalid ticket", "The ticket does not match the artifact.", 10, 68, 1},
			{"unauthorized", "HTTP 401", 10, 68, 1},
			{"unknown error 68", "The validate action failed! Error 68.", 10, 68, 1},
			{"not an exact transport code", "NSURLErrorDomain Code=-10050", 10, 68, 1},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				f := newStaplerFixture(t)
				artifact := filepath.Join(f.dir, "Golder GUI.dmg")
				out, status, calls := f.run(t, tc.failures, tc.message, "valid", "-c", `set -euo pipefail; source ./stapler.sh; stapler_with_retry "$1" "$2"`, "test", action, artifact)
				if status != tc.wantStatus || strings.Count(calls, "xcrun stapler "+action+" "+artifact+"\n") != tc.wantAttempts {
					t.Fatalf("status=%d want=%d attempts=%d want=%d\n%s\n%s", status, tc.wantStatus, strings.Count(calls, "xcrun "), tc.wantAttempts, out, calls)
				}
				for i, delay := range []int{5, 10, 20} {
					got := strings.Contains(calls, fmt.Sprintf("sleep %d\n", delay))
					if want := tc.wantAttempts > i+1; got != want {
						t.Fatalf("incorrect bounded backoff: %s", calls)
					}
				}
				if tc.wantStatus != 0 && !strings.Contains(out, "::error::") {
					t.Fatalf("failure lost its diagnostic: %s", out)
				}
				if tc.wantAttempts > 1 && !strings.Contains(out, tc.message) {
					t.Fatalf("transient failure details lost: %s", out)
				}
			})
		}
	}
}

func TestVerifyMacOSKeepsSignatureAndTeamChecksBeforeNetworkRetry(t *testing.T) {
	for _, signature := range []string{"valid", "invalid", "wrong-team", "ad-hoc"} {
		t.Run(signature, func(t *testing.T) {
			f := newStaplerFixture(t)
			out, status, calls := f.run(t, 1, "NSURLErrorDomain Code=-1005 (lost)", signature,
				"verify.sh", filepath.Join(f.dir, "Golder GUI.app"), filepath.Join(f.dir, "Golder GUI.dmg"), "EXPECTEDTEAM")
			if signature == "valid" {
				if status != 0 || strings.Count(calls, "xcrun stapler validate ") != 2 || !strings.Contains(calls, "codesign --verify --deep --strict --verbose=2 ") {
					t.Fatalf("valid signature did not recover: status=%d\n%s\n%s", status, out, calls)
				}
			} else if status == 0 || strings.Contains(calls, "xcrun ") || strings.Contains(calls, "sleep ") {
				t.Fatalf("invalid identity reached network retries: status=%d\n%s\n%s", status, out, calls)
			}
		})
	}
}
