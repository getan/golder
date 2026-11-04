package larkmcp

import (
	"bytes"
	"context"
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
