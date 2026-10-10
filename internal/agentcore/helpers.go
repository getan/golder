package agentcore

import (
	"context"
	"encoding/json"
	"strings"
)

// ContentToText flattens text blocks of a content list into a single string,
// the lowest-common-denominator representation accepted by every OpenAI-
// compatible gateway. Non-text blocks (thinking, tool calls) are surfaced
// through their own fields, so they are skipped here.
func ContentToText(list ContentList) string {
	var b strings.Builder
	for _, c := range list {
		if tc, ok := c.(TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// LastAssistantOf returns a pointer to the last AssistantMessage in msgs, or nil.
func LastAssistantOf(msgs []AgentMessage) *AssistantMessage {
	for i := len(msgs) - 1; i >= 0; i-- {
		if a, ok := msgs[i].(AssistantMessage); ok {
			return &a
		}
	}
	return nil
}

// EmitFunc emits a loop-level AgentEvent honoring cancellation.
type EmitFunc func(ctx context.Context, ev AgentEvent) error

// PrepareArgumentsFunc optionally rewrites a tool's raw arguments before schema
// validation (e.g. injecting defaults). An error aborts the call with an error
// result. Optional (nil = identity).
type PrepareArgumentsFunc func(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, error)

// BeforeToolCallDecision is the optional result of the beforeToolCall hook. When
// Block is true the tool is not executed and an error result is produced;
// Content/Details override the default block message when set. When Block is
// false, UpdatedInput (when non-empty) replaces the tool's raw arguments before
// execution (PreToolUse rewrite, FR-8; the replacement is re-validated against
// the tool schema) and Sandbox asks for OS-level isolation of the call.
type BeforeToolCallDecision struct {
	Block        bool
	Content      *ContentList
	Details      *any
	UpdatedInput json.RawMessage
	// Sandbox asks the execution layer to run this call under OS-level
	// isolation (e.g. a permission gate graded the command into a sandbox
	// tier). The executor publishes the request into the context the tool
	// executes with (SandboxRequestedFromContext); a tool that cannot isolate
	// must fail closed. Ignored when Block is set.
	Sandbox bool
	// ReadGrant, when non-empty, allows this one call to read the given path
	// outside the workspace. The executor publishes it into the context the
	// tool executes with (ReadGrantFromContext). It carries a single path
	// rather than a directory on purpose: the answer to the boundary question
	// is either "just this one" (this field) or "this directory for the
	// session" (the read-root registry in internal/permissions), which needs
	// no plumbing because every tool reads it directly. Ignored when Block is
	// set.
	ReadGrant string
}

// BeforeToolCallFunc runs after validation and may block the call (permission /
// sandbox checks, FR-4/FR-26). Returning nil allows the call. Optional.
type BeforeToolCallFunc func(ctx context.Context, call AgentToolCall) *BeforeToolCallDecision

// AfterToolCallFunc runs after execution and may override the result
// field-by-field via AfterToolCallResult (FR-5, no deep merge). Optional.
type AfterToolCallFunc func(ctx context.Context, call AgentToolCall, result AgentToolResult, isError bool) *AfterToolCallResult
