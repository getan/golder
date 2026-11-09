package finalizemac

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalizeSignsChecksumBeforePackaging(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity string
		status   string
		wantErr  bool
		mutate   bool
	}{
		{name: "ad hoc", identity: "-"},
		{name: "accepted", identity: "Developer ID Application: Example", status: "Accepted"},
		{name: "rejected", identity: "Developer ID Application: Example", status: "Invalid", wantErr: true},
		{name: "nested code changed", identity: "-", mutate: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, tool := range []string{"bash", "shasum"} {
				if _, err := exec.LookPath(tool); err != nil {
					t.Skipf("%s not available", tool)
				}
			}
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(dir, "Golder GUI.app", "Contents", "Resources", "extensions", "lark", "golder-lark")
			if err := os.MkdirAll(filepath.Dir(helper), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(helper, []byte("signed helper"), 0o755); err != nil {
				t.Fatal(err)
			}
			dmg := filepath.Join(dir, "output.dmg")
			if err := os.WriteFile(dmg, []byte("old image"), 0o600); err != nil {
				t.Fatal(err)
			}
			mocks := map[string]string{
				"codesign": `printf 'codesign %s\n' "$*" >> "$TEST_LOG"
if [[ "$TEST_MUTATE" == true && "$1" == --force ]]; then printf 'changed' >> "$TEST_HELPER"; fi`,
				"hdiutil": `printf 'hdiutil %s\n' "$*" >> "$TEST_LOG"
case "$1" in
  convert) printf 'image' > "${@: -1}" ;;
  attach) mkdir -p "${@: -1}/Golder GUI.app" ;;
esac`,
				"ditto": `printf 'ditto %s\n' "$*" >> "$TEST_LOG"; cp -R "$1" "$2"`,
				"xcrun": `printf 'xcrun %s\n' "$*" >> "$TEST_LOG"
if [[ "$1" == notarytool ]]; then printf '{"id":"test-id","status":"%s"}\n' "$TEST_STATUS"; fi`,
				"jq": `grep -q '"status":"Accepted"'`,
			}
			for name, body := range mocks {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\nset -e\n"+body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(dir, "commands.log")
			app := filepath.Join(dir, "Golder GUI.app")
			cmd := exec.Command("bash", "finalize.sh", app, dmg, tc.identity)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TEST_LOG="+log, "TEST_STATUS="+tc.status, "TEST_HELPER="+helper, fmt.Sprintf("TEST_MUTATE=%t", tc.mutate), "TMPDIR="+dir)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantErr {
				calls, _ := os.ReadFile(log)
				data, _ := os.ReadFile(helper)
				t.Fatalf("finalize: %v\n%s\n%s\nhelper=%q", err, out, calls, data)
			}
			sum := sha256.Sum256([]byte("signed helper"))
			checksum, err := os.ReadFile(helper + ".sha256")
			if err != nil || !strings.HasPrefix(string(checksum), fmt.Sprintf("%x", sum)) {
				t.Fatalf("checksum %q: %v", checksum, err)
			}
			logBytes, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(logBytes)
			if strings.Contains(calls, "codesign --force --deep") || (!tc.mutate && !strings.Contains(calls, "-format UDZO")) {
				t.Fatalf("unexpected signing or image format: %s", calls)
			}
			if tc.mutate && strings.Contains(calls, "hdiutil ") {
				t.Fatalf("packaged a changed nested helper: %s", calls)
			}
			if tc.identity == "-" && strings.Contains(calls, "xcrun ") {
				t.Fatalf("ad-hoc build submitted for notarization: %s", calls)
			}
			if tc.status == "Accepted" && strings.Count(calls, "xcrun stapler staple") != 2 {
				t.Fatalf("missing staple operations: %s", calls)
			}
			if tc.wantErr && strings.Contains(calls, "xcrun stapler staple") {
				t.Fatalf("stapled a rejected submission: %s", calls)
			}
		})
	}
}
