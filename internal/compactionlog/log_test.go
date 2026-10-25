package compactionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/golder/internal/runtime"
)

func TestLogAppendsAJSONLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)

	Log(runtime.CompactionAttempt{
		Timestamp:    time.Date(2026, 10, 25, 5, 45, 15, 0, time.UTC),
		Attempt:      1,
		Reason:       "threshold",
		SessionID:    "s1",
		Model:        "deepseek/deepseek-v4.1-flash",
		Provider:     "commandcode",
		TokensBefore: 807681,
		TokensAfter:  807681,
		Kept:         88,
		DurationMS:   1234,
		Error:        "stream cut",
	})

	path := filepath.Join(home, "logs", "compaction.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got runtime.CompactionAttempt
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, data)
	}
	if got.Attempt != 1 || got.SessionID != "s1" || got.Error != "stream cut" {
		t.Fatalf("decoded attempt = %+v", got)
	}
	if got.TokensBefore != 807681 || got.Kept != 88 || got.DurationMS != 1234 {
		t.Fatalf("attempt counters = %+v", got)
	}
}
