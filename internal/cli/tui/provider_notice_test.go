package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
)

// TestProviderNoticeText pins the wording the user sees while a retry runs: it
// must say this is the provider side, what happened (with the window), and that
// a retry is already in flight. Both languages the transcript follows are
// covered.
func TestProviderNoticeText(t *testing.T) {
	cases := []struct {
		name string
		ev   agentcore.ProviderNoticeEvent
		lang string
		want []string
	}{
		{
			name: "idle zh",
			ev:   agentcore.ProviderNoticeEvent{Provider: "commandcode", Reason: provider.StreamCutIdle, Window: 90 * time.Second},
			lang: "zh",
			want: []string{"provider commandcode", "1m30s", "无数据", "正在重试"},
		},
		{
			name: "stall en",
			ev:   agentcore.ProviderNoticeEvent{Provider: "opencode-go", Reason: provider.StreamCutStall, Window: 108 * time.Second},
			lang: "en",
			want: []string{"provider opencode-go", "1m48s", "no output", "retrying"},
		},
		{
			name: "truncated zh",
			ev:   agentcore.ProviderNoticeEvent{Provider: "commandcode", Reason: provider.StreamCutTruncated},
			lang: "zh",
			want: []string{"provider commandcode", "连接提前关闭", "正在重试"},
		},
		{
			name: "silent en",
			ev:   agentcore.ProviderNoticeEvent{Provider: "commandcode", Reason: provider.StreamCutSilent},
			lang: "en",
			want: []string{"provider commandcode", "without a final event", "retrying"},
		},
		{
			name: "upstream zh",
			ev: agentcore.ProviderNoticeEvent{
				Provider: "openai",
				Reason:   provider.StreamCutUpstream,
				Detail:   "response failed: server_error: boom",
			},
			lang: "zh",
			want: []string{"provider openai", "上游错误", "server_error", "正在重试"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := providerNoticeText(c.ev, c.lang)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("providerNoticeText = %q, want %q", got, want)
				}
			}
		})
	}
}

// TestBridgeProviderNotice verifies the observer event reaches the TUI as the
// dedicated message rather than being dropped by the event switch.
func TestBridgeProviderNotice(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch, nil)
	h.OnEvent(agentcore.ProviderNoticeEvent{
		Kind:     provider.StreamNoticeRetry,
		Provider: "commandcode",
		Reason:   provider.StreamCutIdle,
		Window:   90 * time.Second,
	})

	msgs := drain(ch)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %d, want 1 provider notice", len(msgs))
	}
	pm, ok := msgs[0].(providerNoticeMsg)
	if !ok {
		t.Fatalf("msg = %T, want providerNoticeMsg", msgs[0])
	}
	if pm.ev.Provider != "commandcode" || pm.ev.Reason != provider.StreamCutIdle {
		t.Errorf("notice = %+v, want the bridge to preserve the event", pm.ev)
	}
}
