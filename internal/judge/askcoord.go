package judge

import "sync"

// AskCoord carries one combined answer from the gate that asked it to the gate
// that would otherwise ask the same call's second question. The chain runs
// trust → boundary → permission and cannot pass a context back, so a shared
// per-chain object is the smallest way to keep "one dialog for path + network"
// true without making the boundary gate own the permission gate's decision.
//
// It is keyed by call ID; a call without an ID falls back to the call shape, and
// a miss simply restores today's behavior (the permission gate asks its own
// question).
type AskCoord struct {
	mu   sync.Mutex
	byID map[string]ApprovalAnswer
}

// NewAskCoord returns an empty coordinator.
func NewAskCoord() *AskCoord { return &AskCoord{byID: map[string]ApprovalAnswer{}} }

// Put records the answer for callID.
func (c *AskCoord) Put(callID string, ans ApprovalAnswer) {
	if c == nil || callID == "" {
		return
	}
	c.mu.Lock()
	c.byID[callID] = ans
	c.mu.Unlock()
}

// Take removes and returns the answer for callID.
func (c *AskCoord) Take(callID string) (ApprovalAnswer, bool) {
	if c == nil || callID == "" {
		return ApprovalAnswer{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ans, ok := c.byID[callID]
	delete(c.byID, callID)
	return ans, ok
}
