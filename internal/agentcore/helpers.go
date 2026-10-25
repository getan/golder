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
//
// HasVisibleOutput reports whether an assistant turn says anything the person
// can act on: non-blank text, or a tool call (local or provider-executed).
//
// Reasoning is deliberately NOT output. A message carrying only a thinking
// block has answered nothing and asked for nothing — it is the shape a model
// produces when it exhausts its response on reasoning and then ends the turn —
// and both front-ends and the provider's empty-completion retry need that
// distinction. Asking "is there any content at all?" instead is what let such a
// turn settle in silence: a reasoning block IS content, so the turn looked
// finished, printed nothing, and left the person staring at the last tool card
// with no way to tell "done" from "stalled".
func (m AssistantMessage) HasVisibleOutput() bool {
	if strings.TrimSpace(ContentToText(m.Content)) != "" {
		return true
	}
	return len(m.ToolCalls()) > 0
}

// HasThinking reports whether the turn carried a reasoning block. It exists to
// name the reasoning-only turn when there is no visible output: the two shapes
// (nothing at all, and reasoning with nothing else) need different words, since
// only the first is a protocol mismatch worth checking --model/--base-url for.
func (m AssistantMessage) HasThinking() bool {
	for _, c := range m.Content {
		if tc, ok := c.(ThinkingContent); ok && strings.TrimSpace(tc.Thinking) != "" {
			return true
		}
	}
	return false
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
// false, Sandbox asks for OS-level isolation of the already-validated call.
// PreToolUse argument rewrites happen in PrepareArguments, before this decision.
type BeforeToolCallDecision struct {
	Block   bool
	Content *ContentList
	Details *any
	// Sandbox asks the execution layer to run this call under OS-level
	// isolation (e.g. a permission gate graded the command into a sandbox
	// tier). The executor publishes the request into the context the tool
	// executes with (SandboxRequestedFromContext); a tool that cannot isolate
	// must fail closed. Ignored when Block is set.
	Sandbox bool
	// Network grants outbound network access to this invocation's sandbox. A
	// model's "network:on" argument is only a REQUEST; only the authorization
	// gate can set this bit after a reviewer/user approved the capability.
	Network bool
	// HostControl authorizes this call's explicitly requested access to a pane
	// or browser not created by this agent. It is never a standing grant.
	HostControl bool
	// Direct is a separate per-call escape decision from the authorization gate
	// (reviewer in auto, person in ask/uncertain). An Allow alone cannot set it.
	Direct bool
	// ReadGrant, when non-empty, allows this one call to read the given path
	// outside the workspace. The executor publishes it into the context the
	// tool executes with (ReadGrantFromContext). It carries a single path
	// rather than a directory on purpose: the answer to the boundary question
	// is either "just this one" (this field) or "this directory for the
	// session" (the read-root registry in internal/permissions), which needs
	// no plumbing because every tool reads it directly. Ignored when Block is
	// set.
	ReadGrant string
	// WriteGrant, when non-empty, allows this one call to write the given path
	// outside the workspace: the write side of ReadGrant, set by the boundary
	// gate's "allow this write once" answer to a patch that reaches outside.
	// The executor publishes it into the context the tool executes with
	// (WriteGrantFromContext). Ignored when Block is set.
	WriteGrant string
	// Bare asks the execution layer to run this call OUTSIDE the OS sandbox,
	// because everything the command touches is enumerable from its text (see
	// judge.EnvelopeBare). It is not an escape the model asked for — that is
	// Direct — but the routine case: the gate decided the call may run, and the
	// command's whole effect is readable, so containment buys nothing and costs
	// the call its credentials. The executor publishes it
	// (BareEnvelopeFromContext); the sandbox ladder still wins inside the tool
	// (GOLDER_SANDBOX=enforce contains it anyway). Ignored when Block is set,
	// and never set together with Sandbox.
	Bare bool
	// BareReason is the sentence a note and the card carry when Bare is set.
	BareReason string
}

// BeforeToolCallFunc runs after validation and may block the call (permission /
// sandbox checks, FR-4/FR-26). Returning nil allows the call. Optional.
type BeforeToolCallFunc func(ctx context.Context, call AgentToolCall) *BeforeToolCallDecision

// AfterToolCallFunc runs after execution and may override the result
// field-by-field via AfterToolCallResult (FR-5, no deep merge). Optional.
type AfterToolCallFunc func(ctx context.Context, call AgentToolCall, result AgentToolResult, isError bool) *AfterToolCallResult
