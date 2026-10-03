package cli

import (
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/session"
)

func TestResolveResumeID(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	base := time.Now().UTC()
	mk := func(model string, at time.Time) string {
		h := session.SessionHeader{ID: session.NewID(at), CreatedAt: at, UpdatedAt: at, Model: model}
		if err := store.Save(h, nil); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return h.ID
	}
	idOld := mk("old", base)
	idNew := mk("new", base.Add(time.Second))

	if got, err := ResolveResumeID(store, "1"); err != nil || got != idNew {
		t.Errorf("1 = (%q, %v), want (%q, nil)", got, err, idNew)
	}
	if got, err := ResolveResumeID(store, "2"); err != nil || got != idOld {
		t.Errorf("2 = (%q, %v), want (%q, nil)", got, err, idOld)
	}
	if _, err := ResolveResumeID(store, "3"); err == nil {
		t.Error("3 should be out of range")
	}
	if _, err := ResolveResumeID(store, "0"); err == nil {
		t.Error("0 should be out of range")
	}
	if got, err := ResolveResumeID(store, idOld); err != nil || got != idOld {
		t.Errorf("literal id = (%q, %v), want passthrough", got, err)
	}
	if _, err := ResolveResumeID(store, ""); err == nil {
		t.Error("empty arg should be a usage error")
	}
}

func TestSessionPreview(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	textMsg := func(role string, text string) agentcore.Message {
		c := agentcore.ContentList{agentcore.NewTextContent(text)}
		if role == agentcore.RoleUser {
			return agentcore.UserMessage{RoleField: role, Content: c}
		}
		return agentcore.AssistantMessage{RoleField: role, Content: c}
	}
	save := func(id string, msgs ...agentcore.Message) {
		now := time.Now().UTC()
		h := session.SessionHeader{ID: id, CreatedAt: now, UpdatedAt: now}
		var list agentcore.MessageList
		for _, m := range msgs {
			list = append(list, m)
		}
		if err := store.Save(h, list); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	save("s-user", textMsg(agentcore.RoleUser, "帮我看看这个bug\n第二行"))
	save("s-asst", textMsg(agentcore.RoleAssistant, "assistant only"))
	save("s-empty")
	save("s-long", textMsg(agentcore.RoleUser, "这是一个很长的中文问题描述，用来验证按rune截断不会把汉字切半，后面再补几个字凑够四十"))

	if got := SessionPreview(store, "s-user"); got != "帮我看看这个bug 第二行" {
		t.Errorf("user preview = %q", got)
	}
	if got := SessionPreview(store, "s-asst"); got != "assistant only" {
		t.Errorf("assistant fallback = %q", got)
	}
	if got := SessionPreview(store, "s-empty"); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
	got := SessionPreview(store, "s-long")
	if r := []rune(got); len(r) != previewRunes+1 || string(r[len(r)-1]) != "…" {
		t.Errorf("long preview should be %d runes + …, got %q", previewRunes, got)
	}
	if got := SessionPreview(store, "nope"); got != "" {
		t.Errorf("missing session = %q, want empty", got)
	}

	items, err := RecentSessionsWithPreview(store, 10)
	if err != nil || len(items) != 4 {
		t.Fatalf("RecentSessionsWithPreview = (%d, %v), want (4, nil)", len(items), err)
	}
}
