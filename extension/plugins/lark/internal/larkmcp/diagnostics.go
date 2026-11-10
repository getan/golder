package larkmcp

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	larkversion "github.com/getan/golder/extension/plugins/lark/version"
)

// CLIDiagnostics deliberately excludes raw stderr, environment values, argv
// payloads and auth/setup output. Public CLI error fields are allowlisted;
// unknown free-form stderr remains withheld rather than leaking credentials.
type CLIDiagnostics struct {
	HelperVersion  string   `json:"helper_version"`
	ArgvShape      []string `json:"argv_shape"`
	ExitCode       *int     `json:"exit_code,omitempty"`
	ErrorSource    string   `json:"error_source"`
	CLIType        string   `json:"cli_type,omitempty"`
	LogID          string   `json:"log_id,omitempty"`
	HTTPStatus     int      `json:"http_status,omitempty"`
	RequiredScopes []string `json:"required_scopes,omitempty"`
	MissingScopes  []string `json:"missing_scopes,omitempty"`
	StdoutBytes    int      `json:"stdout_bytes_captured"`
	StderrBytes    int      `json:"stderr_bytes_captured"`
}

var diagnosticCode = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var diagnosticScope = regexp.MustCompile(`^[a-z][a-z0-9_]*:[a-z0-9_.:-]{1,120}$`)
var diagnosticCredential = regexp.MustCompile(`(?i)\b(access[_-]?token|refresh[_-]?token|user[_-]?access[_-]?token|tenant[_-]?access[_-]?token|id[_-]?token|token|app[ _-]?secret|secret|authorization|device[_-]?code|private[_-]?key|api[_-]?key)\b["']?\s*[:=]\s*["']?[^\s,"';}\]]+`)
var diagnosticBearer = regexp.MustCompile(`(?i)\b(Bearer|Basic|DPoP)\s+[A-Za-z0-9+/_.=-]+`)
var diagnosticJWT = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var diagnosticPEM = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
var diagnosticURL = regexp.MustCompile(`https?://[^\s<>"']+`)
var diagnosticFlagError = regexp.MustCompile(`(?mi)(unknown flag|flag needs an argument):\s*(--[a-z][a-z0-9-]{0,63})\b`)

func diagnosticText(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	text = diagnosticBearer.ReplaceAllString(text, "$1 [redacted]")
	text = diagnosticJWT.ReplaceAllString(text, "[redacted JWT]")
	text = diagnosticPEM.ReplaceAllString(text, "[redacted private key]")
	text = diagnosticCredential.ReplaceAllString(text, "$1=[redacted]")
	text = diagnosticURL.ReplaceAllString(text, "[redacted URL]")
	runes := []rune(safeText(text))
	if len(runes) > 2048 {
		return string(runes[:2048]) + "… [truncated]"
	}
	return string(runes)
}

func diagnosticArgv(args []string) []string {
	shape := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			if len(shape) < 2 && len(arg) <= 80 && commandName.MatchString(arg) {
				shape = append(shape, arg)
			} else {
				shape = append(shape, "<redacted positional>")
			}
			continue
		}
		key, value, set := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if len(key) > 80 || !commandName.MatchString(key) {
			shape = append(shape, "<redacted flag>")
			continue
		}
		if !set && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
			value, set = args[i], true
		}
		flag := "--" + key
		if set {
			switch {
			case value == "":
				flag += "=<empty>"
			case key == "as" && value == "user", key == "format" && value == "json":
				flag += "=" + value
			case (key == "mine" || key == "created-by-me") && (value == "true" || value == "false"):
				flag += "=" + value
			default:
				flag += "=<redacted>"
			}
		}
		shape = append(shape, flag)
	}
	return shape
}

func diagnosticObject(data []byte) map[string]any {
	decode := func(data []byte) map[string]any {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var object map[string]any
		if decoder.Decode(&object) != nil {
			return nil
		}
		return object
	}
	isError := func(object map[string]any) bool {
		if problem, ok := object["error"].(map[string]any); ok && len(problem) > 0 {
			return true
		}
		code := diagnosticScalar(object["code"])
		return code != "" && code != "0"
	}
	if object := decode(data); isError(object) {
		return object
	}
	// Notices can precede a pretty-printed JSON error. Only decode object
	// boundaries, never expose the surrounding free-form diagnostic text.
	offset := 0
	for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte{'{'}) {
			object := decode(data[offset:])
			if isError(object) {
				return object
			}
		}
		offset += len(line)
	}
	return nil
}

func diagnosticScalar(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return string(v)
	default:
		return ""
	}
}

func (p *Plugin) withCLIDiagnostics(r Result, args []string, out Output, runErr error) Result {
	if r.OK || r.Error == nil {
		return r
	}
	r.Error.Message = diagnosticText(r.Error.Message, p.Config.AppSecret)
	r.Data = diagnosticData(sanitize(r.Data), p.Config.AppSecret)
	if object := diagnosticObject(out.Stdout); object != nil {
		if _, hasData := object["data"]; !hasData {
			r.Data = nil // do not duplicate arbitrary upstream error fields
		}
	}
	if !diagnosticCode.MatchString(r.Error.Code) || p.Config.AppSecret != "" && strings.Contains(r.Error.Code, p.Config.AppSecret) {
		r.Error.Code = "CLI_ERROR"
	}
	d := &CLIDiagnostics{HelperVersion: larkversion.Version, ArgvShape: diagnosticArgv(args), ErrorSource: "none", StdoutBytes: len(out.Stdout), StderrBytes: len(out.Stderr)}
	if runErr == nil {
		d.ExitCode = &out.Code
	}
	if len(args) >= 2 && args[0] == "drive" && args[1] == "+search" {
		d.RequiredScopes = []string{"search:docs:read"} // pinned CLI v1.0.99 / Search v2
	}
	r.Diagnostics = d
	if out.Truncated {
		d.ErrorSource = "truncated_output_withheld"
		return r
	}
	if r.Error.Code == "IDENTITY_MISMATCH" {
		return r // never replace the adapter's non-user identity refusal
	}
	if runErr != nil {
		d.ErrorSource = "runner_error"
		return r // preserve unavailable/interrupted; no invented exit status
	}
	for _, stream := range []struct {
		name string
		data []byte
	}{{"stdout_json", out.Stdout}, {"stderr_json", out.Stderr}} {
		object := diagnosticObject(stream.data)
		if object == nil {
			continue
		}
		problem, _ := object["error"].(map[string]any)
		if code := diagnosticScalar(object["code"]); problem == nil && code != "" && code != "0" {
			problem = object
		}
		if problem == nil {
			continue
		}
		d.ErrorSource = stream.name
		if code := diagnosticScalar(problem["code"]); out.Code != 10 && code != "" && code != "0" && diagnosticCode.MatchString(code) && (p.Config.AppSecret == "" || !strings.Contains(code, p.Config.AppSecret)) {
			r.Error.Code = code
		}
		if message := diagnosticScalar(problem["message"]); message != "" {
			r.Error.Message = diagnosticText(message, p.Config.AppSecret)
		} else if message := diagnosticScalar(problem["msg"]); message != "" {
			r.Error.Message = diagnosticText(message, p.Config.AppSecret)
		}
		for _, kind := range []string{"validation", "authentication", "authorization", "permission", "network", "api", "internal", "config", "confirmation", "rate_limit", "not_found"} {
			if problem["type"] == kind {
				d.CLIType = kind
			}
		}
		for _, field := range []string{"log_id", "request_id"} {
			value := diagnosticScalar(problem[field])
			if value == "" {
				value = diagnosticScalar(object[field])
			}
			if diagnosticCode.MatchString(value) && (p.Config.AppSecret == "" || !strings.Contains(value, p.Config.AppSecret)) {
				d.LogID = value
				break
			}
		}
		if n, err := strconv.Atoi(diagnosticScalar(problem["http_status"])); err == nil && n >= 100 && n <= 599 {
			d.HTTPStatus = n
		}
		if scopes, ok := problem["missing_scopes"].([]any); ok {
			for _, item := range scopes {
				if scope, ok := item.(string); ok && diagnosticScope.MatchString(scope) && len(d.MissingScopes) < 32 {
					d.MissingScopes = append(d.MissingScopes, scope)
				}
			}
		}
		break
	}
	if d.ErrorSource == "none" && len(out.Stderr) > 0 {
		d.ErrorSource = "unstructured_stderr_withheld"
		if match := diagnosticFlagError.FindSubmatch(out.Stderr); match != nil && out.Code != 10 {
			d.CLIType, d.ErrorSource = "validation", "stderr_argument_error"
			r.Error.Code, r.Error.Message = "CLI_ARGUMENT_ERROR", "Native CLI reports "+string(match[1])+" for "+string(match[2])
		}
	}
	// Keep execution_state conservative, especially for cli_call. An upstream
	// error does not authorize retrying a mutation or starting an auth flow.
	if d.CLIType == "permission" || len(d.MissingScopes) > 0 {
		r.Error.NextAction = "Check the application's required scopes and tenant approval, then explicitly authorize the user via /mcp setup lark. Do not reset login or switch to bot identity."
	} else if d.CLIType == "validation" {
		r.Error.NextAction = "Check diagnostics.argv_shape and cli_help for the installed CLI version; empty argument boundaries are preserved."
	}
	r.Error.Message = diagnosticText(r.Error.Message, p.Config.AppSecret)
	return r
}

func diagnosticData(value any, secret string) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = diagnosticData(item, secret)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = diagnosticData(item, secret)
		}
		return v
	case string:
		return diagnosticText(v, secret)
	default:
		return value
	}
}
