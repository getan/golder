package history

// Tests for the global prompt-history store: round-trip append/load, the
// newest-N read window, tolerance of malformed lines, and size-capped trimming.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), FileName)
}

func TestAppendLoadRoundTrip(t *testing.T) {
	path := tempPath(t)
	want := []Entry{
		{TS: 1, SessionID: "s1", Cwd: "/a", Text: "first prompt"},
		{TS: 2, SessionID: "s1", Cwd: "/a", Text: "second prompt\nwith a newline"},
		{TS: 3, SessionID: "s2", Cwd: "/b", Text: "later session"},
	}
	for _, e := range want {
		if err := Append(path, e); err != nil {
			t.Fatalf("Append(%q): %v", e.Text, err)
		}
	}
	got, err := Load(path, MaxEntries)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Load returned %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.jsonl"), MaxEntries)
	if err != nil {
		t.Fatalf("Load of a missing file must not error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Load of a missing file = %v, want empty", got)
	}
}

func TestLoadCapsAtNewestN(t *testing.T) {
	path := tempPath(t)
	for i := 0; i < 10; i++ {
		if err := Append(path, Entry{TS: int64(i), Text: fmt.Sprintf("p%d", i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := Load(path, 3)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Load returned %d entries, want the newest 3", len(got))
	}
	for i, e := range got {
		if want := fmt.Sprintf("p%d", 7+i); e.Text != want {
			t.Errorf("entry %d = %q, want %q", i, e.Text, want)
		}
	}
}

func TestLoadSkipsMalformedLines(t *testing.T) {
	path := tempPath(t)
	content := `{"ts":1,"text":"ok one"}
not json at all
{"ts":2,"text":"ok two"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := Load(path, MaxEntries)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].Text != "ok one" || got[1].Text != "ok two" {
		t.Fatalf("Load = %+v, want the two well-formed entries", got)
	}
}

func TestAppendTrimsPastCap(t *testing.T) {
	old := maxBytes
	maxBytes = 1024 // tiny cap for the test
	t.Cleanup(func() { maxBytes = old })

	path := tempPath(t)
	// Each entry is ~100 bytes; 40 of them exceed the cap and force a trim.
	for i := 0; i < 40; i++ {
		if err := Append(path, Entry{TS: int64(i), Text: strings.Repeat("x", 60) + fmt.Sprintf("-%02d", i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() > maxBytes {
		t.Errorf("file size = %d, want <= cap %d after trim", info.Size(), maxBytes)
	}
	// Trim keeps the newest entries; the very last write must survive.
	got, err := Load(path, MaxEntries)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("trim left no entries")
	}
	if last := got[len(got)-1].Text; !strings.HasSuffix(last, "-39") {
		t.Errorf("newest entry = %q, want the last append (-39)", last)
	}
}

func TestAppendCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", FileName)
	if err := Append(path, Entry{Text: "secret prompt"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}

// TestWriteAllRoundTrip locks the bulk-write path used by the session-store
// backfill: the file is replaced with exactly the given entries (oldest first)
// and is readable back through Load.
func TestWriteAllRoundTrip(t *testing.T) {
	path := tempPath(t)
	want := []Entry{
		{TS: 1, SessionID: "a", Text: "one"},
		{TS: 2, SessionID: "b", Text: "two\nwith newline"},
	}
	if err := WriteAll(path, want); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	got, err := Load(path, 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
	// A second WriteAll replaces, not appends.
	if err := WriteAll(path, want[:1]); err != nil {
		t.Fatalf("WriteAll (replace): %v", err)
	}
	got, err = Load(path, 0)
	if err != nil {
		t.Fatalf("Load after replace: %v", err)
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Load after replace = %+v, want just the first entry", got)
	}
}
