package larkmcp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExistingApplicationAndPersonalOAuthAreSeparate(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, args []string, input []byte) (Output, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "config init"):
			if strings.Contains(line, "--new") || strings.Contains(line, "private-app-secret") || !strings.Contains(line, "--app-secret-stdin") || string(input) != "private-app-secret\n" {
				t.Fatal("existing app secret did not use stdin")
			}
		case strings.Contains(line, "--no-wait"):
			return Output{Stdout: []byte(`{"verification_url":"https://accounts.feishu.cn/device?user_code=public","device_code":"private-device-code","expires_in":600}`)}, nil
		case strings.HasPrefix(line, "auth status"):
			return Output{Stdout: []byte(`{"appId":"cli_existing","identity":"user","verified":true}`)}, nil
		}
		return Output{Stdout: []byte(`{}`)}, nil
	}}
	p := New(Config{Home: t.TempDir(), AppID: "cli_existing", AppSecret: "private-app-secret", Domains: "docs,im"}, f)
	var events bytes.Buffer
	if err := p.Setup(context.Background(), "lark", &events); err != nil {
		t.Fatal(err)
	}
	text := events.String()
	if !strings.Contains(text, `"type":"authorization"`) || !strings.Contains(text, `"authenticated":true`) || strings.Contains(text, "private-device-code") || strings.Contains(text, "private-app-secret") || len(f.calls) != 4 {
		t.Fatalf("events=%s calls=%v", text, f.calls)
	}
}

func TestLarkPollFailureIsSuccessWhenStatusIsAlreadyVerified(t *testing.T) {
	statusCalls := 0
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "config init"):
			return Output{}, nil
		case strings.Contains(line, "--no-wait"):
			return Output{Stdout: []byte(`{"verification_url":"https://accounts.feishu.cn/device","device_code":"private-device","expires_in":600}`)}, nil
		case strings.Contains(line, "--device-code="):
			return Output{Code: 1, Stderr: []byte("access_token=must-not-leak")}, nil
		case strings.HasPrefix(line, "auth status"):
			statusCalls++
			return Output{Code: 1, Stdout: []byte(`{"identity":"user","verified":true}`), Stderr: []byte("post-save cleanup failed")}, nil
		default:
			return Output{}, nil
		}
	}}
	p := New(Config{Home: t.TempDir(), AppID: "cli_existing", AppSecret: "private", Domains: "docs"}, f)
	var events bytes.Buffer
	if err := p.Setup(context.Background(), "lark", &events); err != nil {
		t.Fatal(err)
	}
	if statusCalls != 1 || !strings.Contains(events.String(), `"type":"complete"`) || strings.Contains(events.String(), "access_token") || strings.Contains(events.String(), "private-device") {
		t.Fatalf("statusCalls=%d events=%s", statusCalls, events.String())
	}
}

func TestLarkPollFailureEmitsSafeFailureWhenStatusIsNotVerified(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "config init"):
			return Output{}, nil
		case strings.Contains(line, "--no-wait"):
			return Output{Stdout: []byte(`{"verification_url":"https://accounts.feishu.cn/device","device_code":"private-device","expires_in":600}`)}, nil
		case strings.Contains(line, "--device-code="):
			return Output{Code: 1, Stderr: []byte("refresh_token=must-not-leak")}, nil
		case strings.HasPrefix(line, "auth status"):
			return Output{Code: 1, Stdout: []byte(`{"identity":"user","verified":false}`)}, nil
		default:
			return Output{}, nil
		}
	}}
	p := New(Config{Home: t.TempDir(), AppID: "cli_existing", AppSecret: "private", Domains: "docs"}, f)
	var events bytes.Buffer
	err := p.Setup(context.Background(), "lark", &events)
	if err == nil || !strings.Contains(events.String(), `"type":"failure"`) || !strings.Contains(events.String(), `"code":"AUTH_POLL_FAILED"`) || strings.Contains(events.String(), "refresh_token") || strings.Contains(events.String(), "private-device") || strings.Contains(events.String(), `"type":"complete"`) {
		t.Fatalf("err=%v events=%s", err, events.String())
	}
}

func TestLarkVerifyRetriesTransientNetworkFailure(t *testing.T) {
	statusCalls := 0
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "config init"):
			return Output{}, nil
		case strings.Contains(line, "--no-wait"):
			return Output{Stdout: []byte(`{"verification_url":"https://accounts.feishu.cn/device","device_code":"private-device","expires_in":600}`)}, nil
		case strings.Contains(line, "--device-code="):
			return Output{}, nil
		case strings.HasPrefix(line, "auth status"):
			statusCalls++
			if statusCalls == 1 {
				return Output{Code: 1, Stderr: []byte("temporary network failure")}, nil
			}
			return Output{Stdout: []byte(`{"identity":"user","verified":true}`)}, nil
		default:
			return Output{}, nil
		}
	}}
	p := New(Config{Home: t.TempDir(), AppID: "cli_existing", AppSecret: "private", Domains: "docs"}, f)
	var waits []time.Duration
	p.wait = func(_ context.Context, delay time.Duration) error { waits = append(waits, delay); return nil }
	var events bytes.Buffer
	if err := p.Setup(context.Background(), "lark", &events); err != nil {
		t.Fatal(err)
	}
	if statusCalls != 2 || len(waits) != 1 || waits[0] != 500*time.Millisecond {
		t.Fatalf("statusCalls=%d waits=%v events=%s", statusCalls, waits, events.String())
	}
}

func TestLarkVerifyRejectsBotWithoutRetry(t *testing.T) {
	statusCalls := 0
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "config init"):
			return Output{}, nil
		case strings.Contains(line, "--no-wait"):
			return Output{Stdout: []byte(`{"verification_url":"https://accounts.feishu.cn/device","device_code":"private-device","expires_in":600}`)}, nil
		case strings.Contains(line, "--device-code="):
			return Output{}, nil
		case strings.HasPrefix(line, "auth status"):
			statusCalls++
			return Output{Stdout: []byte(`{"identity":"bot","verified":true}`)}, nil
		default:
			return Output{}, nil
		}
	}}
	p := New(Config{Home: t.TempDir(), AppID: "cli_existing", AppSecret: "private", Domains: "docs"}, f)
	p.wait = func(context.Context, time.Duration) error { t.Fatal("identity failure was retried"); return nil }
	var events bytes.Buffer
	err := p.Setup(context.Background(), "lark", &events)
	if err == nil || statusCalls != 1 || !strings.Contains(events.String(), `"code":"AUTH_VERIFY_IDENTITY_FAILED"`) || strings.Contains(events.String(), "private-device") {
		t.Fatalf("err=%v statusCalls=%d events=%s", err, statusCalls, events.String())
	}
}

func TestProjectPendingSlowDownAndNoSecretEvents(t *testing.T) {
	polls := 0
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		line := strings.Join(args, " ")
		if strings.Contains(line, "--phase=init") {
			return Output{Stdout: []byte(`{"verification_uri_complete":"https://project.feishu.cn/device?user_code=x","device_code":"private-device","client_id":"private-client","expires_in":600,"interval":2}`)}, nil
		}
		if strings.Contains(line, "--phase=poll") {
			polls++
			status := []string{"pending", "slow_down", "ok"}[polls-1]
			return Output{Stdout: []byte(`{"status":"` + status + `"}`)}, nil
		}
		return Output{Stdout: []byte(`{"authenticated":true}`)}, nil
	}}
	p := New(Config{Home: t.TempDir(), ProjectHost: "project.feishu.cn"}, f)
	var waits []time.Duration
	p.wait = func(_ context.Context, delay time.Duration) error { waits = append(waits, delay); return nil }
	var events bytes.Buffer
	if err := p.Setup(context.Background(), "project", &events); err != nil {
		t.Fatal(err)
	}
	if polls != 3 || len(waits) != 3 || waits[0] != 2*time.Second || waits[2] != 7*time.Second || strings.Contains(events.String(), "private-device") || strings.Contains(events.String(), "private-client") {
		t.Fatalf("polls=%d waits=%v events=%s", polls, waits, &events)
	}
}

func TestProjectDeniedStopsWithoutRestart(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		if strings.Contains(strings.Join(args, " "), "--phase=init") {
			return Output{Stdout: []byte(`{"verification_uri_complete":"https://project.feishu.cn/device","device_code":"private","client_id":"client","expires_in":600,"interval":1}`)}, nil
		}
		return Output{Stdout: []byte(`{"status":"access_denied"}`)}, nil
	}}
	p := New(Config{}, f)
	p.wait = func(context.Context, time.Duration) error { return nil }
	var events bytes.Buffer
	if err := p.Setup(context.Background(), "project", &events); err == nil || len(f.calls) != 2 || strings.Contains(events.String(), `"type":"complete"`) {
		t.Fatalf("err=%v calls=%v events=%s", err, f.calls, &events)
	}
}

func TestProjectSetupReportsActualFailurePhase(t *testing.T) {
	for _, tc := range []struct {
		name, init, poll, status, code, phase string
		initCode, pollCode, statusCode        int
	}{
		{"start", "secret diagnostic", "", "", "AUTH_START_FAILED", "start_authorization", 1, 0, 0},
		{"invalid response", `{}`, "", "", "AUTH_RESPONSE_UNSUPPORTED", "start_authorization", 0, 0, 0},
		{"poll", `{"verification_uri_complete":"https://project.feishu.cn/device","device_code":"secret-device","client_id":"secret-client","expires_in":600,"interval":1}`, `{}`, "", "AUTH_POLL_FAILED", "wait_for_authorization", 0, 1, 0},
		{"verify network", `{"verification_uri_complete":"https://project.feishu.cn/device","device_code":"secret-device","client_id":"secret-client","expires_in":600,"interval":1}`, `{"status":"ok"}`, `{"authenticated":false,"reason":"private network detail"}`, "AUTH_VERIFY_NETWORK_FAILED", "verify_authorization", 0, 0, 2},
		{"verify identity", `{"verification_uri_complete":"https://project.feishu.cn/device","device_code":"secret-device","client_id":"secret-client","expires_in":600,"interval":1}`, `{"status":"ok"}`, `{"authenticated":false}`, "AUTH_VERIFY_IDENTITY_FAILED", "verify_authorization", 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
				line := strings.Join(args, " ")
				switch {
				case strings.Contains(line, "--phase=init"):
					return Output{Code: tc.initCode, Stdout: []byte(tc.init)}, nil
				case strings.Contains(line, "--phase=poll"):
					return Output{Code: tc.pollCode, Stdout: []byte(tc.poll), Stderr: []byte("secret diagnostic")}, nil
				default:
					return Output{Code: tc.statusCode, Stdout: []byte(tc.status)}, nil
				}
			}}
			p := New(Config{}, f)
			p.wait = func(context.Context, time.Duration) error { return nil }
			var events bytes.Buffer
			err := p.Setup(context.Background(), "project", &events)
			text := events.String()
			if err == nil || !strings.Contains(text, `"code":"`+tc.code+`"`) || !strings.Contains(text, `"phase":"`+tc.phase+`"`) || strings.Contains(text, "secret") || strings.Contains(text, "private network detail") {
				t.Fatalf("err=%v events=%s", err, text)
			}
		})
	}
}

func TestProjectInitializationDiagnosticsWithholdPrivateOutput(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic, code string
		runErr                 error
	}{
		{"cli unavailable", "secret-value", "PROJECT_CLI_UNAVAILABLE", errors.New("secret-value")},
		{"network", "dial tcp: lookup secret-value: no such host", "PROJECT_AUTH_NETWORK_FAILED", nil},
		{"tls", "x509: certificate signed by unknown authority secret-value", "PROJECT_AUTH_TLS_FAILED", nil},
		{"other", "secret-value", "AUTH_START_FAILED", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{fn: func(_ string, _ []string, _ []byte) (Output, error) {
				return Output{Code: 1, Stderr: []byte(tc.diagnostic)}, tc.runErr
			}}
			p := New(Config{}, f)
			var events bytes.Buffer
			err := p.Setup(context.Background(), "project", &events)
			if err == nil || !strings.Contains(events.String(), `"code":"`+tc.code+`"`) || strings.Contains(events.String(), "secret-value") {
				t.Fatalf("err=%v events=%s", err, events.String())
			}
		})
	}
}

func TestProjectPollSuccessRetriesOnlyStatusVerification(t *testing.T) {
	polls, checks := 0, 0
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.Contains(line, "--phase=init"):
			return Output{Stdout: []byte(`{"verification_uri_complete":"https://project.feishu.cn/device","device_code":"secret-device","client_id":"secret-client","expires_in":600,"interval":1}`)}, nil
		case strings.Contains(line, "--phase=poll"):
			polls++
			return Output{Code: 1, Stdout: []byte(`{"status":"ok"}`)}, nil
		default:
			checks++
			if checks == 1 {
				return Output{Code: 2, Stdout: []byte(`{"authenticated":false,"reason":"server unreachable"}`)}, nil
			}
			return Output{Stdout: []byte(`{"authenticated":true}`)}, nil
		}
	}}
	p := New(Config{}, f)
	p.wait = func(context.Context, time.Duration) error { return nil }
	var events bytes.Buffer
	if err := p.Setup(context.Background(), "project", &events); err != nil || polls != 1 || checks != 2 || !strings.Contains(events.String(), `"type":"complete"`) {
		t.Fatalf("err=%v polls=%d checks=%d events=%s", err, polls, checks, events.String())
	}
}

func TestProjectUsesBasicVerificationURLAndUserCode(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		switch {
		case strings.Contains(strings.Join(args, " "), "--phase=init"):
			return Output{Stdout: []byte(`{"verification_uri":"https://project.feishu.cn/device","user_code":"ABCD-1234","device_code":"private-device","client_id":"private-client","expires_in":600,"interval":1}`)}, nil
		case strings.Contains(strings.Join(args, " "), "--phase=poll"):
			return Output{Stdout: []byte(`{"status":"ok"}`)}, nil
		default:
			return Output{Stdout: []byte(`{"authenticated":true}`)}, nil
		}
	}}
	p := New(Config{}, f)
	p.wait = func(context.Context, time.Duration) error { return nil }
	var events bytes.Buffer
	if err := p.Setup(context.Background(), "project", &events); err != nil {
		t.Fatal(err)
	}
	text := events.String()
	if !strings.Contains(text, `"verification_url":"https://project.feishu.cn/device"`) || !strings.Contains(text, `"user_code":"ABCD-1234"`) || strings.Contains(text, "private-device") || strings.Contains(text, "private-client") {
		t.Fatalf("events=%s", text)
	}
}

func TestProjectCancellationCannotBecomeVerifiedSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.Contains(line, "--phase=init"):
			return Output{Stdout: []byte(`{"verification_uri_complete":"https://project.feishu.cn/device","device_code":"secret-device","client_id":"secret-client","expires_in":600,"interval":1}`)}, nil
		case strings.Contains(line, "--phase=poll"):
			return Output{Stdout: []byte(`{"status":"ok"}`)}, nil
		default:
			cancel()
			return Output{Stdout: []byte(`{"authenticated":true}`)}, nil
		}
	}}
	p := New(Config{}, f)
	p.wait = func(context.Context, time.Duration) error { return nil }
	var events bytes.Buffer
	if err := p.Setup(ctx, "project", &events); err == nil || strings.Contains(events.String(), `"type":"complete"`) || !strings.Contains(events.String(), `"code":"SETUP_CANCELLED"`) {
		t.Fatalf("err=%v events=%s", err, events.String())
	}
}
