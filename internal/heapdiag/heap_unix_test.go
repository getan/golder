//go:build unix

package heapdiag

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProfileConfiguration(t *testing.T) {
	t.Setenv(envDir, "")
	stop, err := StartFromEnv(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	t.Setenv(envDir, filepath.Join(t.TempDir(), "missing"))
	if _, err := StartFromEnv(io.Discard); err == nil {
		t.Fatal("missing output directory must not enable signal handling")
	}
}

// TestProfileSignal uses a subprocess so an unexpected default SIGUSR2 action
// cannot terminate the test runner. The parent waits for registration first.
func TestProfileSignal(t *testing.T) {
	if os.Getenv("GOLDER_HEAP_TEST_CHILD") == "1" {
		stop, err := StartFromEnv(os.Stderr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer stop()
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProfileSignal$")
	cmd.Env = append(os.Environ(), envDir+"="+dir, "GOLDER_HEAP_TEST_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("child did not register handler: %q: %v", line, err)
	}
	for want := 1; want <= 2; want++ {
		if err := cmd.Process.Signal(syscall.SIGUSR2); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		var files []string
		for time.Now().Before(deadline) {
			files, err = filepath.Glob(filepath.Join(dir, "golder-heap-*.pprof"))
			if err != nil || len(files) == want {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil || len(files) != want {
			t.Fatalf("got %d completed profiles, want %d: %v", len(files), want, err)
		}
		if out, err := exec.Command("go", "tool", "pprof", "-top", files[0]).CombinedOutput(); err != nil {
			t.Fatalf("go tool pprof could not read snapshot: %v\n%s", err, out)
		}
		for _, path := range files {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("profile permissions: %v %v", info, err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			z, err := gzip.NewReader(f)
			if err != nil {
				_ = f.Close()
				t.Fatalf("invalid gzip profile: %v", err)
			}
			n, err := io.Copy(io.Discard, z)
			_ = z.Close()
			_ = f.Close()
			if err != nil || n == 0 {
				t.Fatalf("empty or invalid profile: %d bytes: %v", n, err)
			}
		}
	}
}
