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
			return fail(setupevent.PhaseWaitForAuthorization, setupevent.CodeAuthPollFailed)
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

func (p *Plugin) projectLogin(ctx context.Context, write func(setupevent.Event) error) error {
	base := []string{"auth", "login", "--host=" + p.Config.ProjectHost, "--device-code", "--format=json"}
	out, err := p.Runner.Run(ctx, "project", append(append([]string{}, base...), "--phase=init"), nil)
	if err != nil || out.Code != 0 || out.Truncated {
		return errors.New("could not start Project device authorization; check the installed meegle CLI and tenant host")
	}
	var device struct {
		URL      string `json:"verification_uri_complete"`
		Code     string `json:"device_code"`
		ClientID string `json:"client_id"`
		Expires  int    `json:"expires_in"`
		Interval int    `json:"interval"`
	}
	if json.Unmarshal(out.Stdout, &device) != nil || device.URL == "" || device.Code == "" || device.ClientID == "" || device.Expires <= 0 || device.Expires > 1200 {
		return errors.New("unsupported Project device authorization response")
	}
	if device.Interval < 1 {
		device.Interval = 5
	}
	if device.Interval > 60 {
		return errors.New("unsupported Project authorization interval")
	}
	if err = write(setupevent.Event{Type: "authorization", VerificationURL: device.URL, ExpiresAt: time.Now().Add(time.Duration(device.Expires) * time.Second)}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(device.Expires)*time.Second)
	defer cancel()
	args := append(append([]string{}, base...), "--phase=poll", "--device-code-value="+device.Code, "--client-id="+device.ClientID, "--once")
	interval := time.Duration(device.Interval) * time.Second
	for {
		if err := p.wait(ctx, interval); err != nil {
			return err
		}
		out, err := p.Runner.Run(ctx, "project", args, nil)
		if err != nil || out.Code != 0 || out.Truncated {
			return errors.New("Project authorization failed; retry explicitly after checking network and tenant access")
		}
		var status struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(out.Stdout, &status) != nil {
			return errors.New("unsupported Project authorization poll response")
		}
		switch status.Status {
		case "pending", "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "ok":
			out, err = p.Runner.Run(ctx, "project", []string{"auth", "status", "--format=json"}, nil)
			var verified struct {
				Authenticated bool `json:"authenticated"`
			}
			_ = json.Unmarshal(out.Stdout, &verified)
			if err != nil || out.Code != 0 || !verified.Authenticated {
				return errors.New("Project login was not verified; network/server failure is not proof that reauthentication is required")
			}
			return nil
		default:
			return errors.New("Project authorization was declined or expired; start a new setup explicitly")
		}
	}
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
