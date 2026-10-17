package tui

import (
	"fmt"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
)

// providerNoticeText renders a provider-layer notice in the conversation's
// language. The wording keeps three facts explicit: this is the provider side,
// which failure happened (with the watchdog window where there is one), and that
// a retry is already running — the difference between a wait the user can
// understand and one that looks like a hang.
func providerNoticeText(ev agentcore.ProviderNoticeEvent, lang string) string {
	providerName := ev.Provider
	if providerName == "" {
		providerName = "provider"
	}
	zh := lang == "zh"

	switch ev.Reason {
	case provider.StreamCutIdle:
		if zh {
			return fmt.Sprintf("provider %s 的流中断（%s 无数据），正在重试…", providerName, ev.Window)
		}
		return fmt.Sprintf("provider %s: stream cut (no data for %s); retrying…", providerName, ev.Window)
	case provider.StreamCutStall:
		if zh {
			return fmt.Sprintf("provider %s 的流中断（%s 无输出），正在重试…", providerName, ev.Window)
		}
		return fmt.Sprintf("provider %s: stream cut (no output for %s); retrying…", providerName, ev.Window)
	case provider.StreamCutTruncated:
		if zh {
			return fmt.Sprintf("provider %s 的流中断（连接提前关闭），正在重试…", providerName)
		}
		return fmt.Sprintf("provider %s: stream cut (the connection closed early); retrying…", providerName)
	default:
		// Silent: the stream ended without a final event. This is the case the
		// wire fallback used to surface as a dead end, so name it plainly.
		if zh {
			return fmt.Sprintf("provider %s 的流中断（流在没有结束事件的情况下关闭），正在重试…", providerName)
		}
		return fmt.Sprintf("provider %s: stream cut (the stream ended without a final event); retrying…", providerName)
	}
}
