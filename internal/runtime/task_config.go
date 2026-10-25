package runtime

import (
	"context"
	"maps"
)

type taskRunConfigKey struct{}

// taskRunConfig snapshots inference settings, not the parent's mutable run
// state. Reminder transforms, budgets, persistence callbacks, follow-ups and
// tool registries must never be shared with a child.
type taskRunConfig struct {
	loop          LoopConfig
	sessionID     string
	compactionLog func(CompactionAttempt)
}

func withTaskRunConfig(ctx context.Context, cfg RunConfig) context.Context {
	return context.WithValue(ctx, taskRunConfigKey{}, taskRunConfig{
		loop: LoopConfig{
			Model:         cfg.Model,
			Provider:      cfg.Provider,
			APIKey:        cfg.APIKey,
			GetAPIKey:     cfg.GetAPIKey,
			Stream:        cfg.Stream,
			ThinkingLevel: cfg.ThinkingLevel,
			ContextWindow: cfg.ContextWindow,
			Compaction:    cfg.Compaction,
			SummaryStream: cfg.SummaryStream,
			SummaryModel:  cfg.SummaryModel,
			Extra:         maps.Clone(cfg.Extra),
		},
		sessionID:     cfg.SessionID,
		compactionLog: cfg.CompactionLog,
	})
}

func inheritTaskRunConfig(ctx context.Context, cfg *RunConfig) {
	if parent, ok := ctx.Value(taskRunConfigKey{}).(taskRunConfig); ok {
		cfg.LoopConfig = parent.loop
		cfg.Extra = maps.Clone(parent.loop.Extra)
		// Session affinity and diagnostic correlation only. MemoryRoot and
		// Checkpoint are NOT inherited: a child must not rewrite parent history.
		cfg.SessionID = parent.sessionID
		cfg.CompactionLog = parent.compactionLog
	}
	// A child must not reset or roll over a budget supplied by its factory.
	// runLoop allocates a fresh budget when this field is nil.
	cfg.Budget = nil
}
