package judge

// This file renders the user-facing verdict note (the codex-style approval
// card line): one line stating the decision, the tool, the reviewer's risk /
// authorization rating, and the rationale — written in the language the user
// is using, so the reason is always readable. Colors are applied by the UI
// layer; this only produces plain text.

import "fmt"

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

// FormatNote renders one Note as a single line of plain text. The template
// follows the note's language (zh/en); the rationale is already in that
// language because the reviewer was instructed to write it that way.
func FormatNote(n Note) string {
	tool := n.Tool
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
	rating := fmt.Sprintf("risk: %s, authorization: %s", riskWord(n.Risk, n.Lang), authWord(n.Authorization, n.Lang))
	if n.Lang == "zh" {
		rating = fmt.Sprintf("风险：%s，授权：%s", riskWord(n.Risk, n.Lang), authWord(n.Authorization, n.Lang))
		switch n.Kind {
		case NoteBoundaryGranted:
			return origin + fmt.Sprintf("⚠ 工作区边界放行（%s）：%s", tool, rationale)
		case NoteBoundaryDenied:
			return origin + fmt.Sprintf("✗ 工作区边界：用户拒绝（%s）：%s", tool, n.Summary)
		case NoteBoundaryNoPrompt:
			return origin + fmt.Sprintf("✗ 工作区边界：无人可问，已失败关闭（%s）：%s", tool, n.Summary)
		case NoteApproved:
			return fmt.Sprintf("⚠ 自动审批通过（%s，%s）：%s", tool, rating, rationale)
		case NoteSandboxed:
			return fmt.Sprintf("⚠ 自动审批通过，将在沙箱内运行（%s，%s）：%s", tool, rating, rationale)
		case NoteDenied:
			return fmt.Sprintf("✗ 自动审批拒绝（%s，%s）：%s", tool, rating, rationale)
		case NoteUnavailable:
			return fmt.Sprintf("⚠ 审查不可用，已按保守策略处理（%s）：%s", tool, rationale)
		case NoteBlockedNoPrompt:
			return fmt.Sprintf("✗ 需要确认但没有可用的交互输入，已拒绝（%s，%s）：%s", tool, rating, rationale)
		default:
			return fmt.Sprintf("✗ 只读模式：已阻止 %s", tool)
		}
	}
	switch n.Kind {
	case NoteBoundaryGranted:
		return origin + fmt.Sprintf("⚠ Workspace boundary allowed (%s): %s", tool, rationale)
	case NoteBoundaryDenied:
		return origin + fmt.Sprintf("✗ Workspace boundary: denied by the user (%s): %s", tool, n.Summary)
	case NoteBoundaryNoPrompt:
		return origin + fmt.Sprintf("✗ Workspace boundary: nobody to ask, failed closed (%s): %s", tool, n.Summary)
	case NoteApproved:
		return fmt.Sprintf("⚠ Auto-approved (%s, %s): %s", tool, rating, rationale)
	case NoteSandboxed:
		return fmt.Sprintf("⚠ Auto-approved; will run sandboxed (%s, %s): %s", tool, rating, rationale)
	case NoteDenied:
		return fmt.Sprintf("✗ Auto-review denied (%s, %s): %s", tool, rating, rationale)
	case NoteUnavailable:
		return fmt.Sprintf("⚠ Review unavailable; handled conservatively (%s): %s", tool, rationale)
	case NoteBlockedNoPrompt:
		return fmt.Sprintf("✗ Needs confirmation but no interactive prompt is available; blocked (%s, %s): %s", tool, rating, rationale)
	default:
		return fmt.Sprintf("✗ Read-only mode: blocked %s", tool)
	}
}
