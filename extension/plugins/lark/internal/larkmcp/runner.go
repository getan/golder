package larkmcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const outputBudget = 96 << 10

type Output struct {
	Stdout    []byte
	Stderr    []byte
	Code      int
	Truncated bool
}

type Runner interface {
	Run(context.Context, string, []string, []byte) (Output, error)
}

type NativeRunner struct{ Config Config }

// nativeExecutable refuses npm/JS/shell launchers: some official wrappers fetch
// a platform binary on first invocation, which must never happen implicitly.
func nativeExecutable(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("install the native CLI and configure its absolute path")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("CLI is not installed at its configured path")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return errors.New("CLI is not an executable regular file")
	}
	var magic [4]byte
	if _, err = io.ReadFull(f, magic[:]); err != nil {
		return errors.New("CLI native binary header is invalid")
	}
	n := binary.BigEndian.Uint32(magic[:])
	if string(magic[:]) != "\x7fELF" && n != 0xfeedfacf && n != 0xcffaedfe && n != 0xfeedface && n != 0xcefaedfe && n != 0xcafebabe {
		return errors.New("CLI path is a wrapper, not a native executable; install manually and select the installed platform payload")
	}
	return nil
}

func (r NativeRunner) Run(ctx context.Context, service string, args []string, input []byte) (Output, error) {
	path := r.Config.LarkCLI
	if service == "project" {
		path = r.Config.ProjectCLI
	} else if service != "lark" {
		return Output{}, errors.New("unknown service")
	}
	if err := nativeExecutable(path); err != nil {
		return Output{}, err
	}
	duration := 2 * time.Minute
	if len(args) > 1 && args[0] == "auth" && args[1] == "login" {
		duration = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	// Meegle selects profiles with a root flag, not MEEGLE_PROFILE. Pin it for
	// auth, status, help and business calls alike so they share one file store.
	if service == "project" && r.Config.Profile != "" {
		args = append([]string{"--profile=" + r.Config.Profile}, args...)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Dir = r.Config.Home
	cmd.Env = childEnv(r.Config)
	cmd.WaitDelay = time.Second
	configureChild(cmd)
	var stdout, stderr cappedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Truncated: stdout.truncated || stderr.truncated}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if exit := new(exec.ExitError); errors.As(err, &exit) {
		result.Code = exit.ExitCode()
		return result, nil
	}
	if err != nil {
		return result, errors.New("could not start the configured CLI")
	}
	return result, nil
}

func childEnv(c Config) []string {
	var env []string
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		switch key {
		case "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy":
			env = append(env, pair)
		}
	}
	// A nonexistent, private path disables OS-keychain helper lookup and system
	// browser launch. Both CLI programs are always invoked by absolute path.
	return append(env, "PATH="+filepath.Join(c.Home, ".no-executables"), "HOME="+c.Home, "CFFIXED_USER_HOME="+c.Home,
		"XDG_CONFIG_HOME="+filepath.Join(c.Home, "config"), "XDG_CACHE_HOME="+filepath.Join(c.Home, "cache"), "XDG_DATA_HOME="+filepath.Join(c.Home, "data"), "XDG_STATE_HOME="+filepath.Join(c.Home, "state"),
		"BROWSER=", "MEEGLE_HOST="+c.ProjectHost, "MEEGLE_NO_UPDATE_CHECK=1", "MEEGLE_AI_HANDOFF=disabled",
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1")
}

type cappedBuffer struct {
	data      []byte
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := outputBudget - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *cappedBuffer) Bytes() []byte { return b.data }

type Result struct {
	OK             bool     `json:"ok"`
	Service        string   `json:"service"`
	Identity       string   `json:"identity"`
	Data           any      `json:"data,omitempty"`
	Error          *Failure `json:"error,omitempty"`
	ExecutionState string   `json:"execution_state"`
	Partial        bool     `json:"partial,omitempty"`
	Verification   string   `json:"verification,omitempty"`
	Readback       any      `json:"readback,omitempty"`
}

type Failure struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	NextAction string `json:"next_action,omitempty"`
}

func failure(service, code, message, next string) Result {
	return Result{Service: service, Identity: "user", Error: &Failure{Code: code, Message: message, NextAction: next}, ExecutionState: "not_executed"}
}

func normalize(service string, out Output, err error, write bool) Result {
	state := "failed"
	if write {
		state = "unknown"
	}
	if err != nil {
		r := failure(service, "CLI_UNAVAILABLE", err.Error(), "Check the configured native binary. Install lark-cli with npm install -g @larksuite/cli or meegle with npm install -g @lark-project/meegle; no installer was run.")
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			r.Error.Code = "INTERRUPTED"
			r.ExecutionState = state
		}
		return r
	}
	var payload any
	if json.Unmarshal(out.Stdout, &payload) != nil {
		if out.Code == 0 && !out.Truncated {
			payload = string(out.Stdout)
		} else {
			payload = nil
		}
	}
	r := Result{OK: out.Code == 0, Service: service, Identity: "user", Data: sanitize(payload), ExecutionState: "completed", Partial: out.Truncated}
	if obj, ok := payload.(map[string]any); ok {
		if obj["isError"] == true || obj["error"] != nil {
			r.OK = false
		}
	}
	if object, ok := payload.(map[string]any); ok && service == "lark" {
		if success, present := object["ok"].(bool); present {
			r.OK = r.OK && success
		}
		if id, _ := object["identity"].(string); id != "" && id != "user" {
			return failure(service, "IDENTITY_MISMATCH", "CLI returned a non-user identity; no bot fallback is allowed", "Check the app/user configuration")
		}
		if data, present := object["data"]; present {
			r.Data = sanitize(data)
		}
	}
	if out.Truncated {
		r.OK = false
		r.ExecutionState = state
		r.Error = &Failure{Code: "OUTPUT_LIMIT", Message: "CLI output exceeded its budget; shrink the query/range. Do not repeat a write before verifying its result."}
		return r
	}
	if !r.OK {
		r.ExecutionState = state
		code := "CLI_ERROR"
		message, next := "CLI operation failed", "Use cli_help for this installed CLI version; check application scopes, user authorization and resource access separately"
		// Extract safe public error metadata only; never echo raw stderr, which
		// can contain device codes, headers or credentials.
		if obj, ok := payload.(map[string]any); ok {
			if problem, ok := obj["error"].(map[string]any); ok {
				if c, ok := problem["code"].(string); ok {
					code = c
				}
				if m, ok := problem["message"].(string); ok {
					message = m
				}
			}
		}
		if out.Code == 10 {
			code = "CONFIRMATION_REQUIRED"
			next = "Reconfirm the exact operation; the plugin will not append --yes automatically"
		}
		if service == "project" && len(out.Stdout) == 0 {
			message = fmt.Sprintf("Project CLI failed (exit %d); check project role, field permissions and network", out.Code)
		}
		if strings.Contains(strings.ToLower(code), "auth") || strings.Contains(strings.ToLower(code), "token") {
			next = "Use /mcp setup lark for the corresponding service; do not switch to bot identity"
		}
		r.Error = &Failure{Code: code, Message: safeText(message), NextAction: next}
	}
	return r
}

func sanitize(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			lower := strings.ToLower(strings.ReplaceAll(k, "_", ""))
			if lower == "accesstoken" || lower == "refreshtoken" || lower == "idtoken" || lower == "useraccesstoken" || lower == "tenantaccesstoken" || lower == "privatekey" {
				continue
			}
			if strings.Contains(lower, "secret") || strings.Contains(lower, "authorization") || strings.Contains(lower, "devicecode") || strings.Contains(lower, "verificationuri") || strings.Contains(lower, "verificationurl") {
				continue
			}
			out[k] = sanitize(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = sanitize(v[i])
		}
		return out
	case string:
		return safeText(v)
	default:
		return v
	}
}

func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' || r == 127 {
			return -1
		}
		return r
	}, s)
}
