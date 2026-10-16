package agentcore

// ThinkingLevel is the unified reasoning-effort enum (agent layer). It keeps
// pi's full seven levels; providers map it to their own wire format via a
// per-model ThinkingLevelMap (decision #10).
type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

// ThinkingLevelMap maps a unified level to a provider-specific wire value.
// A nil value means "supported but disabled at this level"; an absent key means
// "this level is not supported by the model". The pointer is what distinguishes
// those two cases, so it must stay *string.
type ThinkingLevelMap map[ThinkingLevel]*string

// AfterToolCallResult is the optional override returned by the afterToolCall
// hook. Every field is a pointer so the loop can distinguish "not provided"
// (nil) from "provided, possibly zero" — pi expresses this with `??`, Go needs
// pointers. Fields are applied with field-level replacement, no deep merge
// (FR-5).
type AfterToolCallResult struct {
	Content   *ContentList
	Details   *any
	Terminate *bool
	IsError   *bool
}
