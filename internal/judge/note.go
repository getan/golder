package judge

// This file renders the user-facing verdict note (the codex-style approval
// card line): one line stating the decision, the tool, the reviewer's risk /
// authorization rating, and the rationale — written in the language the user
// is using, so the reason is always readable. Colors are applied by the UI
// layer; this only produces plain text.

import (
	"fmt"
	"strings"
)

// localized picks the string for the note's language. It exists for the gate's
// OWN sentences — "denied by the user", "no prompt available" — which are the
// only text on a note that did not come from the reviewer (whose rationale is
// already written in the user's language). Spelled here rather than left
// English in a Chinese conversation, where it read as a stray machine string.
func localized(lang, zh, en string) string {
	if lang == "zh" {
		return zh
	}
	return en
}

// Failure reports whether the note records a call that did not run: the two
// renderers paint those in the error color, and a new blocking kind has to be
// added here rather than in both of them.
func (k NoteKind) Failure() bool {
	switch k {
	case NoteDenied, NoteBlockedNoPrompt, NoteReadOnly, NoteBoundaryDenied, NoteBoundaryNoPrompt:
		return true
	}
	return false
}

// riskWord maps a reviewer risk rating to the localized rating word.
func riskWord(risk, lang string) string {
	if lang != "zh" {
		switch risk {
		case "low", "medium", "high", "critical":
			return risk
		default:
			return "unknown"
		}
	}
	switch risk {
	case "low":
		return "低"
	case "medium":
		return "中"
	case "high":
		return "高"
	case "critical":
		return "严重"
	default:
		return "未知"
	}
}

// authWord maps a reviewer authorization rating to the localized word.
func authWord(auth, lang string) string {
	if lang != "zh" {
		switch auth {
		case "low", "medium", "high":
			return auth
		default:
			return "unknown"
		}
	}
	switch auth {
	case "low":
		return "低"
	case "medium":
		return "中"
	case "high":
		return "高"
	default:
		return "未知"
	}
}

// DecisionLine renders the one line every approval surface shares: the tool, the
// approval mode in force, the reviewer's grade, and who settles it —
//
//	bash · ask · confirm → your call
//	bash · auto · contain → contained · runs in the sandbox
//
// The mode and the grade are the canonical tokens the CLI itself uses
// (/permissions auto, the reviewer's verdict) rather than paraphrases, because
// the question this line exists to answer — "does a human have to answer this?"
// — is a question about exactly those two words: confirm under ask is the
// user's to settle, confirm under auto is the reviewer's. What follows the
// arrow is plain language, since that is where the difference lands.
func DecisionLine(tool, mode string, level Level, disposition string) string {
	fields := make([]string, 0, 3)
	if tool != "" {
		fields = append(fields, tool)
	}
	if mode != "" {
		fields = append(fields, mode)
	}
	fields = append(fields, level.String())
	line := strings.Join(fields, " · ")
	if disposition != "" {
		line += " → " + disposition
	}
	return line
}

// AskDisposition says, in one phrase, why a question reached the human instead
// of being settled by review. Under ask every graded call lands here; under auto
// only a handful do, and naming the reason is the whole point — auto is a
// promise not to interrupt, so the one moment it does owes the reader an
// explanation of what made this call an exception.
func AskDisposition(kind ApprovalKind, lang string) string {
	switch kind {
	case ApprovalEscalation:
		return localized(lang, "由你决定 · 沙箱外请求", "your call · sandbox escape")
	case ApprovalReAdmit:
		return localized(lang, "由你决定 · 重准入被拒路径", "your call · re-admit the blocked path")
	case ApprovalNetwork:
		return localized(lang, "由你决定 · 联网", "your call · network")
	case ApprovalBoundary:
		return localized(lang, "由你决定 · 工作区外", "your call · outside the workspace")
	case ApprovalNoSandbox:
		return localized(lang, "由你决定 · 没有沙箱运行器", "your call · no sandbox runner")
	case ApprovalReviewFailed:
		return localized(lang, "由你决定 · 评审不可用", "your call · reviewer unavailable")
	case ApprovalTrust:
		return localized(lang, "首次信任 · 由你决定", "first-run trust · your call")
	default:
		return localized(lang, "由你决定", "your call")
	}
}

// dispositionFor names what actually happened to a call, in the note's language.
// It is the part of the line a reader checks first, and the reason a note no
// longer hides "the reviewer let it run" and "the reviewer contained it" behind
// one "Auto-approved": those are different outcomes, and only one of them runs
// unconfined.
func dispositionFor(n Note) string {
	switch n.Kind {
	case NoteApproved:
		return localized(n.Lang, "已批准", "approved")
	case NoteSandboxed:
		return localized(n.Lang, "已收容 · 沙箱内运行", "contained · runs in the sandbox")
	case NoteEscaped:
		return localized(n.Lang, "沙箱外运行", "runs OUTSIDE the sandbox")
	case NoteDenied:
		if n.Escalation {
			// The call was graded on its own merits AND asked to leave the
			// sandbox. Naming which of the two was refused is the difference
			// between a readable line and "allow → denied".
			return localized(n.Lang, "已拒绝 · 沙箱外请求未获准", "denied · the sandbox escape was not granted")
		}
		return localized(n.Lang, "已拒绝", "denied")
	case NoteUnavailable:
		return localized(n.Lang, "评审不可用 · 按保守策略处理", "review unavailable · handled conservatively")
	case NoteBlockedNoPrompt:
		return localized(n.Lang, "无人可问 · 已阻止", "nobody to ask · blocked")
	case NoteReadOnly:
		return localized(n.Lang, "只读模式 · 已阻止", "read-only mode · blocked")
	case NoteBoundaryGranted:
		return localized(n.Lang, "工作区边界放行", "workspace boundary allowed")
	case NoteBoundaryDenied:
		return localized(n.Lang, "工作区边界 · 用户拒绝", "workspace boundary · denied by the user")
	case NoteBoundaryNoPrompt:
		return localized(n.Lang, "工作区边界 · 无人可问，已失败关闭", "workspace boundary · nobody to ask, failed closed")
	}
	return ""
}

// FormatNote renders one Note as a single line of plain text: the decision line
// (tool · mode · grade → outcome), the reviewer's risk and authorization rating
// when there is one, and the rationale — the last two in the language of the
// conversation, since the reviewer wrote the rationale that way.
func FormatNote(n Note) string {
	rationale := n.Rationale
	if rationale == "" {
		rationale = "-"
	}
	// A question a sub-agent asked is labeled as such: the sink is shared, and a
	// line that reads as coming from the conversation when a delegated child
	// asked it is a line about a call the person never saw.
	origin := ""
	if n.Origin != "" {
		origin = "[" + n.Origin + "] "
	}
	rating := ""
	if n.Risk != "" {
		if n.Lang == "zh" {
			rating = fmt.Sprintf("（风险：%s，授权：%s）", riskWord(n.Risk, n.Lang), authWord(n.Authorization, n.Lang))
		} else {
			rating = fmt.Sprintf(" (risk: %s, authorization: %s)", riskWord(n.Risk, n.Lang), authWord(n.Authorization, n.Lang))
		}
	}
	// The marker leads, so a transcript's notices line up in one column and a
	// denial is scannable without reading the words: Failure() owns which kinds
	// are failures, so a new blocking kind cannot be painted as a warning.
	marker := "⚠ "
	if n.Kind.Failure() {
		marker = "✗ "
	}
	return origin + marker + DecisionLine(n.Tool, n.Mode, n.Level, dispositionFor(n)) + rating + ": " + rationale
}
