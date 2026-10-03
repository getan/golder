// This file implements the context-budget reminder provider: the last link of
// the budget tooling. When the conversation has used most of the model's
// window, the provider injects one <system-reminder> per threshold crossing
// (a notice at <=10% remaining, an escalated warning at <=3%) so the model can
// land the current work and size the next steps to the room it actually has.
//
// Like every reminder it is ephemeral: injected only into that turn's request,
// never persisted, never compacted. The provider is stateless — it reads the
// run's live budget state from the context the loop published, so registering
// it once per session works for every run, parent or sub-agent, and the
// once-per-crossing ladder lives in the shared state.
package runtime

import (
	"context"
	"fmt"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/contextbudget"
)

// BudgetReminderProvider surfaces the remaining context budget near the end of
// a window. Register it with NewReminderRegistry / TodoReminders; it stays
// silent when no budget state is present (e.g. runs outside the loop) or while
// the budget is healthy.
type BudgetReminderProvider struct{}

// Name implements ReminderProvider.
func (p *BudgetReminderProvider) Name() string { return "context-budget" }

// Reminder implements ReminderProvider: it claims the next due threshold
// crossing and renders it. Claiming inside the provider (rather than in the
// loop) keeps the "once per crossing" guarantee next to the state that defines
// the crossing.
func (p *BudgetReminderProvider) Reminder(ctx context.Context, _ agentcore.MessageList) (string, bool) {
	state := contextbudget.FromContext(ctx)
	if state == nil {
		return "", false
	}
	notice, ok := state.TakeNotice()
	if !ok {
		return "", false
	}
	return budgetNoticeBody(notice, state.RolloverEnabled()), true
}

// budgetNoticeBody renders one claimed reminder. The wording escalates with the
// tier and only advertises new_context when the run can actually honor it.
func budgetNoticeBody(n contextbudget.Notice, rolloverEnabled bool) string {
	figures := fmt.Sprintf("about %d tokens remain in this context window (%d of %d used)",
		n.Remaining, n.Used, n.Window)
	if n.Tier == contextbudget.TierCritical {
		body := "Context budget nearly exhausted: " + figures + ". " +
			"Finish the current step now and make the state ready to be carried over: " +
			"record remaining work in the todo tool and leave exact file paths, commands, " +
			"and findings that the next window would need. Do not start new large work."
		if rolloverEnabled {
			body += " When the current task is complete, call new_context to continue in a fresh window."
		}
		return body
	}
	return "Context budget: " + figures + ". " +
		"Keep it in mind while planning: prefer finishing the current task over opening " +
		"large new work that may not fit, and call get_context_remaining for a fresh figure."
}
