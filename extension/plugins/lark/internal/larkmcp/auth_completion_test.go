package larkmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	setupevent "github.com/getan/golder/extension/setup"
)

// Upstream v1.0.99 cmd/auth/login_result.go emits authorization_complete and
// exits non-zero (ExitAuth) AFTER saving the token when some scopes are missing.
// The receipt, not the exit code or an existing login alone, identifies this.
func TestLarkAuthorizationCompletionReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, receipt, status, wantCode string
		code                            int
		truncated                       bool
		runErr                          error
	}{
		{"partial", `{"event":"authorization_complete","user_open_id":"ou_fixture","requested":["calendar:calendar:read","task:task:read"],"granted":["calendar:calendar:read"],"missing":["task:task:read"],"warning":{"type":"missing_scope","hint":"private-server-message"},"access_token":"private-access-token"}`, `{"identity":"user","verified":true}`, setupevent.CodeAuthScopesIncomplete, 3, false, nil},
		{"partial zero exit", `{"event":"authorization_complete","user_open_id":"ou_fixture","missing":["task:task:read"]}`, `{"identity":"user","verified":true}`, setupevent.CodeAuthScopesIncomplete, 0, false, nil},
		{"full nonzero exit", `{"event":"authorization_complete","user_open_id":"ou_fixture","missing":[]}`, `{"identity":"user","verified":true}`, "", 3, false, nil},
		{"failed new flow old login", `{"error":{"type":"authentication","message":"private-server-message"}}`, `{"identity":"user","verified":true}`, setupevent.CodeAuthPollFailed, 3, false, nil},
		{"unverified partial", `{"event":"authorization_complete","user_open_id":"ou_fixture","missing":["task:task:read"]}`, `{"identity":"user","verified":false}`, setupevent.CodeAuthVerifyIdentityFailed, 3, false, nil},
		{"nonuser partial", `{"event":"authorization_complete","user_open_id":"ou_fixture","missing":["task:task:read"]}`, `{"identity":"bot","verified":true}`, setupevent.CodeAuthVerifyIdentityFailed, 3, false, nil},
		{"invalid scope", `{"event":"authorization_complete","user_open_id":"ou_fixture","missing":["https://private.example/device"]}`, `{"identity":"user","verified":true}`, setupevent.CodeAuthPollFailed, 3, false, nil},
		{"no receipt user", `{"event":"authorization_complete","missing":["task:task:read"]}`, `{"identity":"user","verified":true}`, setupevent.CodeAuthPollFailed, 3, false, nil},
		{"truncated receipt", `{"event":"authorization_complete","user_open_id":"ou_fixture","missing":[]}`, `{"identity":"user","verified":true}`, setupevent.CodeAuthPollFailed, 3, true, nil},
		{"interrupted receipt", `{"event":"authorization_complete","user_open_id":"ou_fixture","missing":[]}`, `{"identity":"user","verified":true}`, setupevent.CodeAuthPollFailed, 0, false, errors.New("private-runtime-error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			polls, checks := 0, 0
			f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
				line := strings.Join(args, " ")
				switch {
				case strings.HasPrefix(line, "config init"):
					return Output{}, nil
				case strings.Contains(line, "--no-wait"):
					return Output{Stdout: []byte(`{"verification_url":"https://accounts.feishu.cn/device","device_code":"private-device","expires_in":600}`)}, nil
				case strings.Contains(line, "--device-code="):
					polls++
					if line != "auth login --device-code=private-device --json" {
						t.Fatal("poll must resume the cached device flow without starting or recomputing scopes")
					}
					return Output{Code: tc.code, Truncated: tc.truncated, Stdout: []byte(tc.receipt)}, tc.runErr
				case strings.HasPrefix(line, "auth status"):
					checks++
					return Output{Stdout: []byte(tc.status)}, nil
				default:
					t.Fatalf("unexpected authorization command: %s", args[0])
					return Output{}, nil
				}
			}}
			p := New(Config{Home: t.TempDir(), AppID: "cli_fixture", AppSecret: "private-app-secret", Domains: "calendar,task"}, f)
			var events bytes.Buffer
			err := p.Setup(context.Background(), "lark", &events)
			if polls != 1 || checks != 1 {
				t.Fatalf("poll replay or missing verification: polls=%d checks=%d", polls, checks)
			}
			var terminal setupevent.Event
			for _, line := range bytes.Split(bytes.TrimSpace(events.Bytes()), []byte{'\n'}) {
				if e := json.Unmarshal(line, &terminal); e != nil {
					t.Fatal(e)
				}
			}
			if tc.wantCode == "" {
				if err != nil || terminal.Type != "complete" || !terminal.Authenticated {
					t.Fatalf("completion err=%v event=%+v", err, terminal)
				}
			} else if err == nil || terminal.Type != "failure" || terminal.Code != tc.wantCode {
				t.Fatalf("failure err=%v event=%+v, want %s", err, terminal, tc.wantCode)
			}
			if tc.wantCode == setupevent.CodeAuthScopesIncomplete {
				if terminal.Retryable || len(terminal.MissingScopes) != 1 || terminal.MissingScopes[0] != "task:task:read" || !strings.Contains(terminal.Message, "Login completed") {
					t.Fatalf("partial consent was not actionable: %+v", terminal)
				}
			}
			for _, secret := range []string{"private-device", "private-access-token", "private-app-secret", "private-server-message", "private-runtime-error", "private.example", "ou_fixture"} {
				if strings.Contains(events.String(), secret) {
					t.Fatalf("setup exposed %s", secret)
				}
			}
		})
	}
}
