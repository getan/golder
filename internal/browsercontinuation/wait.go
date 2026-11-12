package browsercontinuation

import "encoding/json"

// WaitingTarget accepts only the structured identity emitted by the trusted
// browser implementation, not textual errors or arbitrary tool output.
func WaitingTarget(details any) (string, uint64, bool) {
	raw, err := json.Marshal(details)
	if err != nil {
		return "", 0, false
	}
	var wait struct {
		Waiting    bool   `json:"browser_wait"`
		Browser    string `json:"browser_wait_id"`
		Generation uint64 `json:"browser_wait_generation"`
	}
	if json.Unmarshal(raw, &wait) != nil || !wait.Waiting || wait.Browser == "" || wait.Generation == 0 {
		return "", 0, false
	}
	return wait.Browser, wait.Generation, true
}
