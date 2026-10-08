// This file implements batch tool execution (US-005): a batch of tool calls
// from one assistant message is split into ordered groups, mirroring pi's
// semantics while letting independent calls overlap the way codex does.
//
//   - Calls are first grouped (see batchGroups): a maximal run of parallel-safe
//     calls becomes one group whose members run concurrently, and every
//     sequential call stands alone. Groups run one after another in source
//     order, so a side-effecting call still observes everything before it and
//     is observed by everything after it — the ordering guarantee the old
//     all-or-nothing rule gave, without stalling the reads around it.
//   - The whole batch signals termination only when every finalized result has
//     terminate=true, matching pi.
//   - Abort stops further groups and fills the remaining calls with aborted
//     error results so every call still gets a result message.
//
// Results are backfilled at their source index, so completion order never
// reorders the batch. (prepare is not separately staged: executeToolCall keeps
// prepare+execute together per call.)
package agenttool

import (
	"context"
	"sync"

	"github.com/getan/golder/internal/agentcore"
)

// ForceSequential, when true, makes the whole batch run serially regardless of
// per-tool ExecutionMode.
type BatchConfig struct {
	ToolExecutorConfig
	ForceSequential bool
}

// ExecuteToolCalls runs a batch of tool calls belonging to one assistant
// message. It returns the tool-result messages in source order and whether the
// whole batch requests termination (only when every result terminates).
func ExecuteToolCalls(ctx context.Context, cfg BatchConfig, calls []agentcore.AgentToolCall, emit agentcore.EmitFunc) ([]agentcore.ToolResultMessage, bool) {
	if len(calls) == 0 {
		return nil, false
	}

	results := make([]agentcore.ToolResultMessage, len(calls))
	terminates := make([]bool, len(calls))

	for _, group := range batchGroups(cfg.Registry, calls, cfg.ForceSequential) {
		if ctx.Err() != nil {
			// Abort: fill the remaining calls with aborted error results so
			// every tool call still gets a result message.
			for _, i := range group {
				results[i] = errorToolResult(calls[i], "tool call aborted")
				terminates[i] = false
			}
			continue
		}
		if len(group) == 1 {
			i := group[0]
			results[i], terminates[i] = executeToolCall(ctx, cfg.ToolExecutorConfig, calls[i], emit)
			continue
		}
		var wg sync.WaitGroup
		for _, i := range group {
			wg.Add(1)
			go func(i int, call agentcore.AgentToolCall) {
				defer wg.Done()
				results[i], terminates[i] = executeToolCall(ctx, cfg.ToolExecutorConfig, call, emit)
			}(i, calls[i])
		}
		wg.Wait()
	}

	// Whole batch terminates only when every result terminates (pi semantics).
	allTerminate := true
	for _, t := range terminates {
		if !t {
			allTerminate = false
			break
		}
	}
	return results, allTerminate
}

// batchGroups splits a batch into the ordered groups the executor runs: a
// maximal run of parallel-safe calls is one group (members run concurrently),
// and a sequential call is always a group of its own. forceSequential makes
// every call its own group, the global serial switch.
//
// This is the codex read/write split expressed as ordered groups rather than a
// shared lock: the concurrency a batch gets is exactly "the independent calls
// in it overlap", while the relative order of the side-effecting calls — and
// their position between the reads — stays deterministic instead of depending
// on which goroutine reached the lock first.
func batchGroups(reg *ToolRegistry, calls []agentcore.AgentToolCall, forceSequential bool) [][]int {
	var groups [][]int
	parallelRun := false
	for i, call := range calls {
		if !forceSequential && !requiresSequential(reg, call) {
			if parallelRun && len(groups) > 0 {
				groups[len(groups)-1] = append(groups[len(groups)-1], i)
			} else {
				groups = append(groups, []int{i})
			}
			parallelRun = true
			continue
		}
		groups = append(groups, []int{i})
		parallelRun = false
	}
	return groups
}

// requiresSequential reports whether one call's tool declares sequential
// execution, which makes it a barrier around the parallel calls next to it.
// An unregistered tool is treated as sequential: it cannot run, and letting it
// share a concurrent group would only make the failure ordering unclear.
func requiresSequential(reg *ToolRegistry, call agentcore.AgentToolCall) bool {
	tool, ok := reg.Get(call.Name)
	if !ok {
		return true
	}
	return tool.ExecutionMode() == agentcore.ToolExecutionSequential
}
