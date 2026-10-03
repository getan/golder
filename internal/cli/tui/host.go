// This file makes runSession satisfy cli.Host: the accessor and mutator methods
// let the /status command (and future /goal, /btw) read the session's live
// collaborators and mutable state through the cli.Host contract rather than the
// concrete aggregate — the same seam the REPL's replDeps implements. The
// compile-time assertion below fails the build if runSession drifts out of
// conformance.
package tui

import (
	"bufio"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/hooks"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/plugin"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
	"github.com/getan/golder/internal/trust"
)

var _ cli.Host = (*runSession)(nil)

func (s *runSession) Store() *session.Store                      { return s.store }
func (s *runSession) Header() session.SessionHeader              { return s.header }
func (s *runSession) AgentCtx() *agentcore.AgentContext          { return s.agentCtx }
func (s *runSession) Live() *cli.LiveConfig                      { return s.live }
func (s *runSession) Registry() *agenttool.ToolRegistry          { return s.reg }
func (s *runSession) Reminders() *runtime.ReminderRegistry       { return s.reminders }
func (s *runSession) Slash() *runtime.SlashRegistry              { return s.slash }
func (s *runSession) Creds() *provider.CredentialStore           { return s.creds }
func (s *runSession) Notifier() *plugin.EventNotifier            { return nil }
func (s *runSession) NotifierHandle() func(agentcore.AgentEvent) { return s.onEvent }
func (s *runSession) Trust() *trust.Manager                      { return s.trust }
func (s *runSession) Permissions() *permissions.State            { return s.perms }
func (s *runSession) ReviewNotes() *run.ReviewNotes              { return s.notes }
func (s *runSession) Goal() *agenttool.GoalState                 { return nil }
func (s *runSession) Telemetry() *cli.TelemetryHolder            { return s.telemetry }
func (s *runSession) Dispatcher() *hooks.Dispatcher              { return s.dispatcher }
func (s *runSession) HookDeps() run.HookDeps                     { return s.hookDeps }
func (s *runSession) Cwd() string                                { return s.cwd }
func (s *runSession) Input() *bufio.Reader                       { return nil }
func (s *runSession) ConfirmMu() *sync.Mutex                     { return nil }

func (s *runSession) CurLeaf() string      { return s.curLeaf }
func (s *runSession) SetCurLeaf(id string) { s.curLeaf = id }
func (s *runSession) Persisted() int       { return s.persisted }
func (s *runSession) SetPersisted(n int)   { s.persisted = n }

func (s *runSession) LastBtw() *agentcore.AgentContext       { return s.lastBtw }
func (s *runSession) SetLastBtw(ctx *agentcore.AgentContext) { s.lastBtw = ctx }
func (s *runSession) LastBtwBase() int                       { return s.lastBtwBase }
func (s *runSession) SetLastBtwBase(n int)                   { s.lastBtwBase = n }

// renderSession writes the /session summary (US-009, #125) to out — the same
// format the REPL's runSession prints: session id, message count, estimated
// token usage, model/provider, creation time, and compaction-checkpoint count.
// It lives on runSession so the TUI's /session intercept and the REPL share one
// rendering; counts derive from the in-memory context (the source of truth for
// the live turn), so unsaved messages are counted too.
func (s *runSession) renderSession(out io.Writer) {
	msgs := s.agentCtx.Messages
	tokens := compaction.EstimateContextTokens(msgs).Tokens
	compactions := 0
	for _, m := range msgs {
		if _, ok := m.(agentcore.CompactionMessage); ok {
			compactions++
		}
	}
	fmt.Fprintf(out, "session:      %s\n", s.header.ID)
	fmt.Fprintf(out, "messages:     %d\n", len(msgs))
	fmt.Fprintf(out, "tokens (est): %d\n", tokens)
	model := s.live.Model
	providerName := s.live.ProviderName
	if model == "" {
		model = s.header.Model
	}
	if providerName == "" {
		providerName = s.header.Provider
	}
	fmt.Fprintf(out, "model:        %s (provider: %s)\n", model, providerName)
	if !s.header.CreatedAt.IsZero() {
		fmt.Fprintf(out, "created:      %s\n", s.header.CreatedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "compactions:  %d\n", compactions)
}
