package larkmcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	setupevent "github.com/getan/golder/extension/setup"
)

// Setup emits only operator display fields. Device codes and client IDs remain
// in this process and are not tool arguments, logs or MCP results.
func (p *Plugin) Setup(ctx context.Context, service string, output io.Writer) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if service != "lark" && service != "project" {
		return errors.New("choose setup service lark or project")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	write := func(event setupevent.Event) error {
		event.Version = setupevent.Version
		event.Service = service
		return json.NewEncoder(output).Encode(event)
	}
	fail := func(phase, code string) error {
		event, failureErr := setupevent.FailureEvent(phase, code)
		if failureErr != nil {
			return failureErr
		}
		if failureErr = write(event); failureErr != nil {
			return failureErr
		}
		return errors.New(event.Message)
	}
	failContext := func() error {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail(setupevent.PhaseComplete, setupevent.CodeSetupTimeout)
		}
		return fail(setupevent.PhaseComplete, setupevent.CodeSetupCancelled)
	}
	if err := write(setupevent.Event{Type: "notice", Message: "Preparing user authorization; application configuration and personal OAuth are separate"}); err != nil {
		return err
	}
	if service == "lark" {
		if err := prepareLarkStore(p.Config.Home); err != nil {
			return fail(setupevent.PhasePrepareStore, setupevent.CodePrepareStoreFailed)
		}
		if p.Config.AppID != "" || p.Config.AppSecret != "" {
			if p.Config.AppID == "" || p.Config.AppSecret == "" {
				return fail(setupevent.PhaseConfigureApplication, setupevent.CodeAppConfigFailed)
			}
			// Do not pass the App Secret in argv or create another app. The
			// official CLI encrypts it in the source's own credential store.
			out, err := p.Runner.Run(ctx, "lark", []string{"config", "init", "--app-id=" + p.Config.AppID, "--app-secret-stdin", "--brand=feishu"}, []byte(p.Config.AppSecret+"\n"))
			if err != nil || out.Code != 0 || out.Truncated {
				if ctx.Err() != nil {
					return failContext()
				}
				return fail(setupevent.PhaseConfigureApplication, setupevent.CodeAppConfigFailed)
			}
		} else {
			out, err := p.Runner.Run(ctx, "lark", []string{"auth", "status", "--json"}, nil)
			var status map[string]any
			_ = json.Unmarshal(out.Stdout, &status)
			if err != nil || out.Code != 0 || status["appId"] == nil {
				if ctx.Err() != nil {
					return failContext()
				}
				return fail(setupevent.PhaseConfigureApplication, setupevent.CodeAppConfigFailed)
			}
		}
		out, err := p.Runner.Run(ctx, "lark", []string{"auth", "login", "--domain=" + p.Config.Domains, "--no-wait", "--json"}, nil)
		if err != nil || out.Code != 0 || out.Truncated {
			if ctx.Err() != nil {
				return failContext()
			}
			return fail(setupevent.PhaseStartAuthorization, setupevent.CodeAuthStartFailed)
		}
		var device struct {
			URL     string `json:"verification_url"`
			Code    string `json:"device_code"`
			Expires int    `json:"expires_in"`
		}
		if json.Unmarshal(out.Stdout, &device) != nil || device.URL == "" || device.Code == "" || device.Expires <= 0 || device.Expires > 1200 {
			return fail(setupevent.PhaseStartAuthorization, setupevent.CodeAuthResponseUnsupported)
		}
		if err = write(setupevent.Event{Type: "authorization", VerificationURL: device.URL, ExpiresAt: time.Now().Add(time.Duration(device.Expires) * time.Second)}); err != nil {
			return err
		}
		pollCtx, stop := context.WithTimeout(ctx, time.Duration(device.Expires)*time.Second)
		defer stop()
		out, err = p.Runner.Run(pollCtx, "lark", []string{"auth", "login", "--device-code=" + device.Code, "--json"}, nil)
		verifiedAfterPollFailure := false
		if err != nil || out.Code != 0 || out.Truncated {
			if ctx.Err() != nil {
				return failContext()
			}
			verified, verifyCode := p.verifyLarkUser(ctx)
			if !verified {
				if verifyCode == setupevent.CodeAuthVerifyNetworkFailed {
					return fail(setupevent.PhaseVerifyAuthorization, verifyCode)
				}
				return fail(setupevent.PhaseWaitForAuthorization, setupevent.CodeAuthPollFailed)
			}
			verifiedAfterPollFailure = true
		}
		if !verifiedAfterPollFailure {
			verified, verifyCode := p.verifyLarkUser(ctx)
			if !verified {
				if ctx.Err() != nil {
					return failContext()
				}
				return fail(setupevent.PhaseVerifyAuthorization, verifyCode)
			}
		}
	} else {
		if err := p.projectLogin(ctx, write); err != nil {
			if ctx.Err() != nil {
				return failContext()
			}
			var failure *projectSetupFailure
			if errors.As(err, &failure) {
				return fail(failure.phase, failure.code)
			}
			return err
		}
	}
	return write(setupevent.Event{Type: "complete", Authenticated: true})
}

// verifyLarkUser accepts a positive verified payload even if the CLI exits
// non-zero after saving it. An explicit non-user/unverified payload is final;
// otherwise bounded read-only retries cover command/network failures without
// retrying authorization or widening identity.
func (p *Plugin) verifyLarkUser(ctx context.Context) (bool, string) {
	delays := []time.Duration{0, 500 * time.Millisecond, time.Second}
	for attempt, delay := range delays {
		if delay > 0 {
			if err := p.wait(ctx, delay); err != nil {
				return false, setupevent.CodeAuthVerifyNetworkFailed
			}
		}
		out, err := p.Runner.Run(ctx, "lark", []string{"auth", "status", "--json", "--verify"}, nil)
		if ctx.Err() != nil {
			return false, setupevent.CodeAuthVerifyNetworkFailed
		}
		var status struct {
			Identity string `json:"identity"`
			Verified *bool  `json:"verified"`
		}
		parsed := !out.Truncated && json.Unmarshal(out.Stdout, &status) == nil
		if parsed && status.Identity == "user" && status.Verified != nil && *status.Verified {
			return true, ""
		}
		if parsed && (status.Identity != "" || status.Verified != nil) {
			return false, setupevent.CodeAuthVerifyIdentityFailed
		}
		if err != nil || out.Code != 0 {
			if attempt+1 < len(delays) {
				continue
			}
			return false, setupevent.CodeAuthVerifyNetworkFailed
		}
		return false, setupevent.CodeAuthVerifyIdentityFailed
	}
	return false, setupevent.CodeAuthVerifyNetworkFailed
}

type projectSetupFailure struct{ phase, code string }

func (e *projectSetupFailure) Error() string { return e.code }

func projectFailure(phase, code string) error {
	return &projectSetupFailure{phase: phase, code: code}
}

// Classify only fixed diagnostic categories; never return CLI output, which
// may contain device codes, request URLs or other private material.
func projectStartFailure(out Output, err error) string {
	if err != nil {
		return setupevent.CodeProjectCLIUnavailable
	}
	if out.Truncated {
		return setupevent.CodeAuthStartFailed
	}
	diagnostic := strings.ToLower(string(out.Stdout) + "\n" + string(out.Stderr))
	for _, marker := range []string{"x509:", "certificate verify failed", "certificate signed by unknown authority", "tls handshake"} {
		if strings.Contains(diagnostic, marker) {
			return setupevent.CodeProjectAuthTLSFailed
		}
	}
	for _, marker := range []string{"no such host", "network is unreachable", "connection refused", "connection reset", "proxyconnect", "i/o timeout", "server unreachable"} {
		if strings.Contains(diagnostic, marker) {
			return setupevent.CodeProjectAuthNetworkFailed
		}
	}
	return setupevent.CodeAuthStartFailed
}

// projectSeconds accepts the CLI's JSON integer as well as a decimal string,
// without accepting fractional, overflowing or otherwise ambiguous values.
type projectSeconds int64

func (s *projectSeconds) UnmarshalJSON(raw []byte) error {
	text := string(raw)
	if len(text) > 0 && text[0] == '"' {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return err
		}
		text = decoded
	}
	value, err := strconv.ParseInt(text, 10, 32)
	if err != nil {
		return err
	}
	*s = projectSeconds(value)
	return nil
}

func (p *Plugin) projectLogin(ctx context.Context, write func(setupevent.Event) error) error {
	base := []string{"auth", "login", "--host=" + p.Config.ProjectHost, "--device-code", "--format=json"}
	out, err := p.Runner.Run(ctx, "project", append(append([]string{}, base...), "--phase=init"), nil)
	if err != nil || out.Code != 0 || out.Truncated {
		return projectFailure(setupevent.PhaseStartAuthorization, projectStartFailure(out, err))
	}
	var device struct {
		URL      string         `json:"verification_uri_complete"`
		BasicURL string         `json:"verification_uri"`
		UserCode string         `json:"user_code"`
		Code     string         `json:"device_code"`
		ClientID string         `json:"client_id"`
		Expires  projectSeconds `json:"expires_in"`
		Interval projectSeconds `json:"interval"`
	}
	if json.Unmarshal(out.Stdout, &device) != nil {
		return projectFailure(setupevent.PhaseStartAuthorization, setupevent.CodeProjectResponseInvalid)
	}
	if device.Code == "" {
		return projectFailure(setupevent.PhaseStartAuthorization, setupevent.CodeProjectDeviceMissing)
	}
	if device.ClientID == "" {
		return projectFailure(setupevent.PhaseStartAuthorization, setupevent.CodeProjectClientMissing)
	}
	if device.Expires <= 0 {
		return projectFailure(setupevent.PhaseStartAuthorization, setupevent.CodeProjectExpiryInvalid)
	}
	userCode := ""
	if device.URL == "" {
		if device.BasicURL == "" {
			return projectFailure(setupevent.PhaseStartAuthorization, setupevent.CodeProjectURLMissing)
		}
		if !setupevent.AuthorizationCode(device.UserCode) {
			return projectFailure(setupevent.PhaseStartAuthorization, setupevent.CodeProjectUserCodeInvalid)
		}
		device.URL, userCode = device.BasicURL, device.UserCode
	}
	if device.Interval < 1 {
		device.Interval = 5
	}
	if device.Interval > 540 {
		return projectFailure(setupevent.PhaseStartAuthorization, setupevent.CodeProjectIntervalInvalid)
	}
	// Never extend the server's lifetime. The helper's overall setup limit is
	// ten minutes, so a longer-lived server code is safely bounded to nine.
	lifetime := time.Duration(min(device.Expires, 540)) * time.Second
	if time.Duration(device.Interval)*time.Second >= lifetime {
		return projectFailure(setupevent.PhaseStartAuthorization, setupevent.CodeProjectIntervalInvalid)
	}
	if err = write(setupevent.Event{Type: "authorization", VerificationURL: device.URL, UserCode: userCode, ExpiresAt: time.Now().Add(lifetime)}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, lifetime)
	defer cancel()
	args := append(append([]string{}, base...), "--phase=poll", "--device-code-value="+device.Code, "--client-id="+device.ClientID, "--once")
	interval := time.Duration(device.Interval) * time.Second
	for {
		if err := p.wait(ctx, interval); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return projectFailure(setupevent.PhaseComplete, setupevent.CodeSetupTimeout)
			}
			return projectFailure(setupevent.PhaseWaitForAuthorization, setupevent.CodeAuthPollFailed)
		}
		out, err := p.Runner.Run(ctx, "project", args, nil)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return projectFailure(setupevent.PhaseComplete, setupevent.CodeSetupTimeout)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil || out.Truncated {
			return projectFailure(setupevent.PhaseWaitForAuthorization, setupevent.CodeAuthPollFailed)
		}
		var status struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(out.Stdout, &status) != nil {
			return projectFailure(setupevent.PhaseWaitForAuthorization, setupevent.CodeAuthPollFailed)
		}
		switch status.Status {
		case "pending", "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "ok":
			verified, code := p.verifyProjectUser(ctx)
			if err := ctx.Err(); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					return projectFailure(setupevent.PhaseComplete, setupevent.CodeSetupTimeout)
				}
				return err
			}
			if !verified {
				return projectFailure(setupevent.PhaseVerifyAuthorization, code)
			}
			return nil
		default:
			return projectFailure(setupevent.PhaseWaitForAuthorization, setupevent.CodeAuthPollFailed)
		}
	}
}

// Verify only after this flow's poll reports success. A pre-existing login
// must never turn a declined or failed new authorization into success.
func (p *Plugin) verifyProjectUser(ctx context.Context) (bool, string) {
	delays := []time.Duration{0, 500 * time.Millisecond, time.Second}
	for attempt, delay := range delays {
		if delay > 0 {
			if err := p.wait(ctx, delay); err != nil {
				return false, setupevent.CodeAuthVerifyNetworkFailed
			}
		}
		out, err := p.Runner.Run(ctx, "project", []string{"auth", "status", "--format=json"}, nil)
		if ctx.Err() != nil {
			return false, setupevent.CodeAuthVerifyNetworkFailed
		}
		var status struct {
			Authenticated *bool `json:"authenticated"`
		}
		parsed := !out.Truncated && json.Unmarshal(out.Stdout, &status) == nil && status.Authenticated != nil
		if err == nil && out.Code == 0 && parsed && *status.Authenticated {
			return true, ""
		}
		if err == nil && out.Code == 1 && parsed && !*status.Authenticated {
			return false, setupevent.CodeAuthVerifyIdentityFailed
		}
		if attempt+1 == len(delays) {
			return false, setupevent.CodeAuthVerifyNetworkFailed
		}
	}
	return false, setupevent.CodeAuthVerifyNetworkFailed
}

func prepareLarkStore(home string) error {
	if runtime.GOOS != "darwin" {
		return nil
	} // official Linux store is file-backed
	dir := filepath.Join(home, "Library", "Application Support", "lark-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "master.key.file")
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || st.Size() != 32 {
			return errors.New("private Lark file key is invalid; do not overwrite an existing credential store")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("private credential store already contains files without its local key; reset/re-authorize instead of exporting global keychain material")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err == nil {
		_, err = f.Write(key)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	return closeErr
}
