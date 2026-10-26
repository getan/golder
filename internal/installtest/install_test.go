// Package installtest tests installer dependency detection with fake package
// managers. No test downloads packages, invokes real sudo, or alters the host.
package installtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBubblewrapInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell installer")
	}
	script, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Load only definitions, not the download/install entry point.
	definitions, _, ok := strings.Cut(string(script), "# 1. 检测下载器")
	if !ok {
		t.Fatal("installer definitions marker missing")
	}
	for _, tc := range []struct {
		name, os, pm, uid                             string
		existing, sudo, fail, missingBinary, wantFail bool
		want                                          []string
	}{
		{name: "macOS", os: "Darwin"},
		{name: "existing", os: "Linux", existing: true},
		{name: "apt", os: "Linux", pm: "apt-get", want: []string{"apt-get update", "apt-get install -y --no-install-recommends bubblewrap"}},
		{name: "dnf sudo", os: "Linux", pm: "dnf", uid: "1000", sudo: true, want: []string{"sudo dnf install -y bubblewrap", "dnf install -y bubblewrap"}},
		{name: "yum", os: "Linux", pm: "yum", want: []string{"yum install -y bubblewrap"}},
		{name: "arch", os: "Linux", pm: "pacman", want: []string{"pacman -S --needed --noconfirm bubblewrap"}},
		{name: "suse", os: "Linux", pm: "zypper", want: []string{"zypper --non-interactive install bubblewrap"}},
		{name: "alpine", os: "Linux", pm: "apk", want: []string{"apk add bubblewrap"}},
		{name: "unsupported", os: "Linux", wantFail: true},
		{name: "no sudo", os: "Linux", pm: "apt-get", uid: "1000", wantFail: true},
		{name: "package failure", os: "Linux", pm: "dnf", fail: true, wantFail: true},
		{name: "apt update failure", os: "Linux", pm: "apt-get", fail: true, wantFail: true},
		{name: "package did not supply bwrap", os: "Linux", pm: "dnf", missingBinary: true, wantFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			log := filepath.Join(t.TempDir(), "calls")
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			uid := tc.uid
			if uid == "" {
				uid = "0"
			}
			write("id", "printf '%s\\n' "+uid)
			if tc.existing {
				write("bwrap", "exit 0")
			}
			if tc.sudo {
				write("sudo", `printf 'sudo %s\n' "$*" >>"$CALL_LOG"; exec "$@"`)
			}
			if tc.pm != "" {
				body := `printf '` + tc.pm + ` %s\n' "$*" >>"$CALL_LOG"` + "\n"
				switch {
				case tc.fail:
					body += "exit 1"
				case !tc.missingBinary:
					body += `if [ "$1" != update ]; then printf '#!/bin/sh\nexit 0\n' >"$PATH/bwrap"; /bin/chmod +x "$PATH/bwrap"; fi`
				}
				write(tc.pm, body)
			}
			cmd := exec.Command("/bin/sh", "-c", definitions+"\nOS="+tc.os+"\nensure_bubblewrap\n")
			cmd.Env = append(os.Environ(), "PATH="+bin, "CALL_LOG="+log)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantFail {
				t.Fatalf("error = %v, want failure %v; output: %s", err, tc.wantFail, out)
			}
			if tc.os == "Linux" && !strings.Contains(string(out), "强依赖 bubblewrap") {
				t.Errorf("Linux installer must explain the required dependency: %s", out)
			}
			if tc.pm != "" && !tc.existing && !strings.Contains(string(out), "也可先自行安装 bubblewrap") {
				t.Errorf("installer must offer manual installation before requesting privileges: %s", out)
			}
			calls, _ := os.ReadFile(log)
			for _, want := range tc.want {
				if !strings.Contains(string(calls), want+"\n") {
					t.Errorf("missing %q in calls %q", want, calls)
				}
			}
			if len(tc.want) == 0 && !tc.fail && !tc.missingBinary && len(calls) != 0 {
				t.Errorf("unexpected package-manager calls: %s", calls)
			}
		})
	}
}
