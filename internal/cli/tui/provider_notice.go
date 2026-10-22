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
	progress := ""
	if ev.Attempt > 0 {
		if zh {
			progress = fmt.Sprintf("（%d/%d）", ev.Attempt, ev.MaxAttempts)
		} else {
			progress = fmt.Sprintf(" (%d/%d)", ev.Attempt, ev.MaxAttempts)
		}
	}
	// The watchdog's evidence ("12 keep-alives, last line: comment") is the
	// difference between a gateway that holds the socket open and a dead
	// connection, so the notice carries it when the guard counted anything.
	detail := ""
	if ev.Detail != "" {
		if zh {
			detail = "；" + ev.Detail
		} else {
			detail = "; " + ev.Detail
		}
	}

	switch ev.Reason {
	case provider.StreamCutIdle:
		if zh {
			return fmt.Sprintf("provider %s 的流中断（%s 无数据%s），正在重试%s…", providerName, ev.Window, detail, progress)
		}
		return fmt.Sprintf("provider %s: stream cut (no data for %s%s); retrying%s…", providerName, ev.Window, detail, progress)
	case provider.StreamCutStall:
		if zh {
			return fmt.Sprintf("provider %s 的流中断（%s 无输出%s），正在重试%s…", providerName, ev.Window, detail, progress)
		}
		return fmt.Sprintf("provider %s: stream cut (no output for %s%s); retrying%s…", providerName, ev.Window, detail, progress)
	case provider.StreamCutTruncated:
		if zh {
			return fmt.Sprintf("provider %s 的流中断（连接提前关闭%s），正在重试%s…", providerName, detail, progress)
		}
		return fmt.Sprintf("provider %s: stream cut (the connection closed early%s); retrying%s…", providerName, detail, progress)
	case provider.StreamCutUpstream:
		// The connection was healthy; the server behind it failed the response.
		// Saying "stream cut" here would blame the wrong side of the wire.
		if zh {
			return fmt.Sprintf("provider %s 上游错误%s，正在重试%s…", providerName, detail, progress)
		}
		return fmt.Sprintf("provider %s: upstream error%s; retrying%s…", providerName, detail, progress)
	default:
		// Silent: the stream ended without a final event. This is the case the
		// wire fallback used to surface as a dead end, so name it plainly.
		if zh {
			return fmt.Sprintf("provider %s 的流中断（流在没有结束事件的情况下关闭%s），正在重试%s…", providerName, detail, progress)
		}
		return fmt.Sprintf("provider %s: stream cut (the stream ended without a final event%s); retrying%s…", providerName, detail, progress)
	}
}
