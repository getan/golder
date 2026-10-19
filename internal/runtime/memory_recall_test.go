package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Recall ships off: a disabled provider never consults the store.
func TestMemoryRecallDisabled(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("global", "notes.md"): "permission deadlock during checkpoint save",
	})
	p := &MemoryReminderProvider{Store: st, Disabled: true}
	if _, ok := p.Reminder(context.Background(), userMsgs("permission deadlock checkpoint")); ok {
		t.Fatal("a disabled provider must not fire")
	}
}

// A one-unit query ("继续", "ok") is not a question; it must not recall.
func TestMemoryRecallSkipsShortQueries(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("global", "notes.md"): "继续 修 记忆 召回 的 体验",
	})
	p := &MemoryReminderProvider{Store: st, MinUnits: 2, MinOverlap: 0}
	for _, q := range []string{"继续", "ok", "好的"} {
		if _, ok := p.Reminder(context.Background(), userMsgs(q)); ok {
			t.Errorf("%q: a one-unit query must not recall", q)
		}
	}
	if got := MemoryRecallStats().LastReason; got != "query too short to recall on" {
		t.Errorf("last reason = %q, want the short-query skip", got)
	}
}

// A hit has to share enough of the query: the store's relative floor always
// keeps a top hit, so "something matched" is not "this is relevant".
func TestMemoryRecallRequiresOverlap(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("global", "shopping.md"): "grocery shopping list for the weekend",
	})
	p := &MemoryReminderProvider{Store: st, MinUnits: 2, MinOverlap: 2}
	if _, ok := p.Reminder(context.Background(), userMsgs("how do I handle the permission deadlock?")); ok {
		t.Fatal("an unrelated match must not be injected")
	}
	if got := MemoryRecallStats().LastReason; got != "matches shared too little of the query" && got != "no match" {
		t.Errorf("last reason = %q, want the relevance skip", got)
	}

	st2 := openMemoryStore(t, map[string]string{
		filepath.Join("global", "auth.md"): "permission deadlock during checkpoint save",
	})
	p2 := &MemoryReminderProvider{Store: st2, MinUnits: 2, MinOverlap: 2}
	if _, ok := p2.Reminder(context.Background(), userMsgs("how do I handle the permission deadlock?")); !ok {
		t.Fatal("a relevant hit should be injected")
	}
}

// The reconcile is throttled: a file written between turns is invisible until
// the interval elapses (the walk is linear in the number of memory files and
// used to run every turn).
func TestMemoryRecallThrottlesReconcile(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("global", "a.md"): "alpha beta gamma",
	})
	p := &MemoryReminderProvider{Store: st, MinUnits: 2, MinOverlap: 1, ReconcileInterval: time.Hour}
	if _, ok := p.Reminder(context.Background(), userMsgs("alpha beta")); !ok {
		t.Fatal("first turn should see the first file")
	}
	if err := os.WriteFile(filepath.Join(st.Root(), "global", "b.md"), []byte("delta epsilon zeta"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Reminder(context.Background(), userMsgs("delta epsilon")); ok {
		t.Fatal("a throttled turn must not reconcile a newly written file")
	}
	p.ReconcileInterval = 0
	if _, ok := p.Reminder(context.Background(), userMsgs("delta epsilon zeta")); !ok {
		t.Fatal("with the throttle off the new file should be indexed and matched")
	}
}

func TestRecallUnits(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"继续", 1},
		// 8 runes → 7 bigrams. No dictionary is available here, so bigrams
		// cross word boundaries ("下记", "忆的"): that only ever adds overlap,
		// and the memory text is measured the same way.
		{"修一下记忆的召回", 7},
		{"ok", 1},
		{"permission deadlock", 2},
		{"a", 0},
		{"记忆……记忆", 1}, // deduped bigram
		{"go test ./...", 2},
	} {
		if got := len(recallUnits(tc.query)); got != tc.want {
			t.Errorf("recallUnits(%q) = %d units (%v), want %d", tc.query, got, recallUnits(tc.query), tc.want)
		}
	}
}

func TestMemoryRecallStatsRecordTheFire(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("global", "auth.md"): "permission deadlock during checkpoint save",
	})
	p := &MemoryReminderProvider{Store: st, MinUnits: 2, MinOverlap: 2}
	if _, ok := p.Reminder(context.Background(), userMsgs("permission deadlock")); !ok {
		t.Fatal("expected a fire")
	}
	s := MemoryRecallStats()
	if !s.LastFired || s.LastReason != "injected" || len(s.LastPaths) == 0 {
		t.Fatalf("stats = %+v, want an injected decision with its paths", s)
	}
	if s.LastQuery != "permission deadlock" {
		t.Errorf("last query = %q", s.LastQuery)
	}
}

// A live toggle reaches providers already registered, so /memory recall off
// applies to the running session.
func TestSetMemoryRecallTogglesTheRegistry(t *testing.T) {
	st := openMemoryStore(t, map[string]string{
		filepath.Join("global", "auth.md"): "permission deadlock during checkpoint save",
	})
	reg := NewReminderRegistry(&MemoryReminderProvider{Store: st, MinUnits: 2, MinOverlap: 2})
	if !reg.SetMemoryRecall(false) {
		t.Fatal("a registry with a memory provider should report the toggle")
	}
	if msgs := reg.Messages(context.Background(), userMsgs("permission deadlock")); len(msgs) != 0 {
		t.Fatalf("a disabled registry injected %d reminders", len(msgs))
	}
	reg.SetMemoryRecall(true)
	if msgs := reg.Messages(context.Background(), userMsgs("permission deadlock")); len(msgs) != 1 {
		t.Fatalf("re-enabled registry injected %d reminders, want 1", len(msgs))
	}
}
