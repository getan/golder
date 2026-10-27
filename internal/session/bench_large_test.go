package session

import (
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// largeMessages builds roughly three megabytes of history, each message
// carrying enough text to make a full-history scan measurable.
func largeMessages(n int) agentcore.MessageList {
	body := strings.Repeat("ordinary build output line\n", 180) +
		"password = correcthorsebattery\n"
	msgs := make(agentcore.MessageList, n)
	for i := range msgs {
		msgs[i] = agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(body)},
		}
	}
	return msgs
}

func BenchmarkAppendBranchLarge(b *testing.B) {
	store, err := NewStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	msgs := largeMessages(600)
	h := SessionHeader{ID: "bench", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.Save(h, msgs); err != nil {
		b.Fatal(err)
	}
	_, entries, err := store.LoadEntries(h.ID)
	if err != nil {
		b.Fatal(err)
	}
	leaf := entries[len(entries)-1].ID
	one := agentcore.MessageList{agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent("one more turn")},
	}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next, err := store.AppendBranch(h, leaf, one)
		if err != nil {
			b.Fatal(err)
		}
		leaf = next
	}
}

func BenchmarkLoadEntriesLarge(b *testing.B) {
	store, err := NewStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	h := SessionHeader{ID: "bench", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.Save(h, largeMessages(600)); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := store.LoadEntries(h.ID); err != nil {
			b.Fatal(err)
		}
	}
}
