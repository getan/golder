package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

func writeSessionLines(t *testing.T, dir, id string, lines ...[]byte) string {
	t.Helper()
	path := filepath.Join(dir, FileName(id))
	var b strings.Builder
	for _, line := range lines {
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRepairOversizedLine(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	header := SessionHeader{Version: SchemaVersion, ID: "big", CreatedAt: now, UpdatedAt: now}
	hraw, _ := json.Marshal(header)
	normal := Entry{
		ID: "e1", Timestamp: now,
		Message: agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
	}
	nraw, _ := json.Marshal(normal)
	huge := Entry{
		ID: "e2", ParentID: "e1", Timestamp: now,
		Message: agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("x", sessionScanBufMax+1024))},
		},
	}
	hrawBig, _ := json.Marshal(huge)
	path := writeSessionLines(t, store.Dir(), "big", hraw, nraw, hrawBig)

	if _, _, err := store.LoadEntries("big"); !errors.Is(err, ErrSessionLineTooLong) {
		t.Fatalf("LoadEntries error = %v, want ErrSessionLineTooLong", err)
	}

	report, err := store.RepairOversized("big")
	if err != nil {
		t.Fatalf("RepairOversized: %v", err)
	}
	if report.Omitted != 1 || report.Lines != 2 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Stat(path + ".pre-repair"); err != nil {
		t.Fatalf("backup: %v", err)
	}

	_, entries, err := store.LoadEntries("big")
	if err != nil {
		t.Fatalf("LoadEntries after repair: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[1].ID != "e2" || entries[1].ParentID != "e1" {
		t.Fatalf("tree fields = %+v", entries[1])
	}
	um, ok := entries[1].Message.(agentcore.UserMessage)
	if !ok {
		t.Fatalf("message = %T", entries[1].Message)
	}
	if text := agentcore.ContentToText(um.Content); !strings.Contains(text, "oversized message omitted") {
		t.Fatalf("placeholder = %q", text)
	}
}

func TestLoadEntriesRefusesAnOversizedFile(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	header := SessionHeader{Version: SchemaVersion, ID: "huge", CreatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(header)
	path := writeSessionLines(t, store.Dir(), "huge", raw)
	if err := os.Truncate(path, SessionMaxLoadBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadEntries("huge"); !errors.Is(err, ErrSessionTooLarge) {
		t.Fatalf("LoadEntries error = %v, want ErrSessionTooLarge", err)
	}
}
