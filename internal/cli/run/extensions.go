package run

import (
	"context"
	"fmt"
	"os"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/runtime"
)

type SetupOptions struct{ NoExtensions bool }

// InstallExtensions publishes a complete catalog at turn boundaries, on the
// loop goroutine. Both model declarations and execution see the same registry.
func InstallExtensions(cfg *runtime.RunConfig, m *extensions.Manager, prompt runtime.PromptInputs) {
	if m == nil || cfg.Batch.Registry == nil {
		return
	}
	observer := cfg.ObserveEvent
	cfg.ObserveEvent = func(ev agentcore.AgentEvent) {
		if observer != nil {
			observer(ev)
		}
		m.Observe(ev)
	}
	reg := cfg.Batch.Registry
	var base []agentcore.AgentTool
	for _, t := range reg.List() {
		if _, ok := t.(interface{ ManagedExtension() }); !ok {
			base = append(base, t)
		}
	}
	last := ^uint64(0)
	prior := cfg.SyncTools
	cfg.SyncTools = func(ctx context.Context, ac *agentcore.AgentContext) {
		if prior != nil {
			prior(ctx, ac)
		}
		m.RefreshDirty(ctx)
		rev := m.Revision()
		if last == rev {
			return
		}
		all := append(append([]agentcore.AgentTool{}, base...), m.Tools()...)
		if e := reg.ReplaceAll(all); e != nil {
			fmt.Fprintf(os.Stderr, "golder: extension catalog rejected: %v\n", e)
			return
		}
		ac.Tools = reg.List()
		if prompt.WorkingDir != "" {
			p := prompt
			p.Tools = ac.Tools
			p.Skills = m.Skills()
			if text, e := p.Build(); e == nil {
				ac.SystemPrompt = text
			}
		}
		last = rev
	}
}
