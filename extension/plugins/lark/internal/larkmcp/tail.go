package larkmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var commandName = regexp.MustCompile(`^[+]?[a-z][a-z0-9._-]*$`)

func tailSchema(execute bool) json.RawMessage {
	key := "command"
	if execute {
		key = "argv"
	}
	props := map[string]any{"service": map[string]any{"type": "string", "enum": []string{"lark", "project"}}, key: map[string]any{
		"type": "array", "minItems": 0, "maxItems": 64,
		"description": "Inline business arguments. For cli_raw, argv may also start with api <METHOD> <PATH>; omit --as/--format/--yes and use inline --data/--params JSON. Omit --as, --format, --yes, --force and --no-confirm; the adapter controls identity, output format and confirmation. For cli_help, an empty command returns root help and a trailing --help/-h is accepted. cli_help only covers business commands; it cannot query auth/config/install. Skill CLI examples are not MCP capabilities.",
		"items":       map[string]any{"type": "string", "maxLength": 16000},
	}}
	required := []string{"service", key}
	if execute {
		props[key].(map[string]any)["minItems"] = 1
		props["confirm"] = map[string]any{"type": "boolean", "const": true}
		props["high_risk"] = map[string]any{"type": "boolean", "default": false, "description": "Set only when the CLI command is high-risk. The adapter appends --yes after exact-call host approval."}
		props["path"] = text("cli_raw api only: optional workspace-relative local path for {file} in --file or --output. Output without path defaults to .golder/artifacts/lark-api-*.bin")
		required = append(required, "confirm")
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required})
	return b
}

func (p *Plugin) tail(ctx context.Context, name string, raw json.RawMessage) Result {
	var in struct {
		Service  string   `json:"service"`
		Command  []string `json:"command"`
		Argv     []string `json:"argv"`
		Path     string   `json:"path"`
		Confirm  bool     `json:"confirm"`
		HighRisk bool     `json:"high_risk"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return failure("", "INVALID_ARGUMENTS", "Invalid long-tail request", "Use cli_help first")
	}
	if name == "cli_help" {
		if in.Service != "lark" && in.Service != "project" {
			return failure(in.Service, "INVALID_ARGUMENTS", "cli_help needs service lark or project", "Use cli_help with service=lark or service=project; no command was executed")
		}
		command := stripHelpSuffix(in.Command)
		if len(command) == 0 {
			args := []string{"--help"}
			out, err := p.Runner.Run(ctx, in.Service, args, nil)
			return p.withCLIDiagnostics(normalize(in.Service, out, err, false), args, out, err)
		}
		if err := validateCommandPath(in.Service, command, false); err != nil {
			return failure(in.Service, "COMMAND_BLOCKED", err.Error(), "Use an empty command for root help, or a business/raw command family; for login or incremental scopes use Golder's Lark/Project authorization/settings entry; skill CLI examples are not MCP tools; no command was executed")
		}
		args := append(append([]string{}, command...), "--help")
		out, err := p.Runner.Run(ctx, in.Service, args, nil)
		return p.withCLIDiagnostics(normalize(in.Service, out, err, false), args, out, err)
	}
	if !in.Confirm {
		return failure(in.Service, "INTENT_REQUIRED", "Confirm the exact CLI operation before execution", "Host approval is separate")
	}
	if !hostAuthorized(ctx) {
		return failure(in.Service, "OPERATOR_CONFIRMATION_REQUIRED", "Long-tail execution requires exact-call host policy authorization", "Use the configured host approval chain; auto uses the reviewer and ask uses the person's decision")
	}
	if len(in.Argv) < 2 {
		return failure(in.Service, "COMMAND_BLOCKED", "Provide a business command and its operation", "Use cli_help")
	}
	business := name == "cli_call"
	if in.Service == "lark" && len(in.Argv) > 0 && in.Argv[0] == "api" {
		if business {
			return failure("lark", "COMMAND_BLOCKED", "api is a raw escape hatch, not a Lark +shortcut", "Use cli_raw with service=lark for api <METHOD> <PATH>; the command was not executed")
		}
		return p.rawAPI(ctx, in.Argv, in.Path, in.HighRisk)
	}
	if in.Path != "" {
		return failure(in.Service, "INVALID_ARGUMENTS", "path is only accepted for cli_raw api output or upload", "Omit path unless argv starts with api and uses {file}")
	}
	pathLen := 2
	if !business {
		pathLen = commandPathLen(in.Argv, 3)
	}
	if err := validateCommandPath(in.Service, in.Argv[:pathLen], business); err != nil {
		next := "Use cli_help for this installed CLI version; no command was executed"
		if business && in.Service == "lark" && len(in.Argv) >= 2 && !strings.HasPrefix(in.Argv[1], "+") {
			next = "cli_call only takes Lark +shortcuts; use cli_raw for raw resource commands such as drive files list. The command was not executed."
		}
		if !business && in.Service == "lark" && len(in.Argv) >= 2 && strings.HasPrefix(in.Argv[1], "+") {
			next = "cli_raw is for raw resource commands; use cli_call for +shortcuts. The command was not executed."
		}
		return failure(in.Service, "COMMAND_BLOCKED", err.Error(), next)
	}
	if err := businessFlags(in.Service, in.Argv[:pathLen], in.Argv[pathLen:]); err != nil {
		return failure(in.Service, "ARGUMENT_BLOCKED", err.Error(), "Omit --as/--format/confirmation flags; use only approved inline business parameters, not local-file or configuration references")
	}
	if in.HighRisk {
		// The model never supplies --yes. The adapter adds it only after the
		// exact-call host authorization above, mirroring dedicated write tools.
		in.Argv = append(in.Argv, "--yes")
	}
	// These two fixed commands are known reads. This only affects result
	// classification; every long-tail call still requires exact-call approval.
	knownRead := name == "cli_call" && ((in.Service == "lark" && in.Argv[0] == "drive" && in.Argv[1] == "+search") ||
		(in.Service == "project" && in.Argv[0] == "url" && in.Argv[1] == "decode"))
	return p.run(ctx, in.Service, in.Argv, !knownRead)
}

// stripHelpSuffix accepts the form callers naturally copy from CLI examples
// (`drive +export --help`) instead of forcing a second help syntax on them.
func stripHelpSuffix(command []string) []string {
	for len(command) > 0 {
		last := command[len(command)-1]
		if last != "--help" && last != "-h" {
			break
		}
		command = command[:len(command)-1]
	}
	return command
}

func validateCommandPath(service string, parts []string, business bool) error {
	if service != "lark" && service != "project" {
		return errors.New("unknown service")
	}
	if len(parts) == 0 || len(parts) > 3 {
		return errors.New("provide a command family and optional resource/operation")
	}
	for _, part := range parts {
		if !commandName.MatchString(part) {
			return errors.New("invalid command path")
		}
	}
	switch parts[0] {
	case "auth", "config", "install":
		return errors.New(parts[0] + " is not an MCP CLI capability")
	}
	if business && service == "lark" {
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "+") {
			return errors.New("Lark business calls use +shortcuts; use cli_raw for raw resource commands")
		}
	}
	return nil
}

// commandPathLen splits an argv into its non-flag command path. Raw resource
// commands have two or three non-flag segments before the first --flag.
func commandPathLen(argv []string, max int) int {
	n := 2
	for n < len(argv) && n < max && !strings.HasPrefix(argv[n], "--") {
		n++
	}
	return n
}

type businessFlag struct {
	key, value string
}

func businessFlags(service string, command, args []string) error {
	flags, err := parseBusinessFlags(args)
	if err != nil {
		return err
	}
	for _, flag := range flags {
		key, value := flag.key, flag.value
		switch key {
		case "as":
			return errors.New("--as is controlled by the adapter; omit --as (the adapter appends --as=user)")
		case "format":
			return errors.New("--format is controlled by the adapter; omit it")
		case "yes", "force", "no-confirm":
			return errors.New("confirmation flags are controlled by the adapter; use confirm=true and the host approval chain")
		}
		if strings.Contains(key, "access-token") || strings.Contains(key, "refresh-token") || strings.Contains(key, "user-token") {
			return fmt.Errorf("--%s is a credential flag and is not exposed", key)
		}
		if resourceTokenFlag(key) {
			if value == "" {
				return fmt.Errorf("--%s requires an inline resource token", key)
			}
			if !validResourceValue(value, remoteResourceIsList(key)) {
				return fmt.Errorf("--%s must be an inline resource token, not a local file, URL or stdin reference", key)
			}
			continue
		}
		if blockedFlagKey(key) {
			return fmt.Errorf("--%s is a configuration, credential, or local-file flag and is not exposed", key)
		}
		if !localPathFlag(key) && strings.Contains(value, "{file}") {
			return fmt.Errorf("--%s is not a local path parameter; {file} is only for file_transfer", key)
		}
		if key == "file-name" && !validOutputFileName(value) {
			return fmt.Errorf("--%s must be a plain filename without a directory or traversal", key)
		}
		if strings.HasPrefix(strings.TrimSpace(value), "@") || value == "-" || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("local-file/stdin references are not allowed")
		}
		if strings.Contains(key, "url") && strings.Contains(value, "://") {
			if err := resourceURL(value); err != nil {
				return err
			}
		}
	}
	_ = service
	_ = command
	return nil
}

func parseBusinessFlags(args []string) ([]businessFlag, error) {
	out := make([]businessFlag, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || a == "--" {
			return nil, errors.New("only explicit long flags are accepted")
		}
		key, value, equals := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !commandName.MatchString(key) {
			return nil, errors.New("invalid flag name")
		}
		if !equals && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
			value = args[i]
		}
		out = append(out, businessFlag{key: key, value: value})
	}
	return out, nil
}

func resourceTokenFlag(key string) bool {
	return key == "token" || key == "folder-tokens" ||
		strings.HasSuffix(key, "-token") || strings.HasSuffix(key, "_token") ||
		strings.HasSuffix(key, "-token-list") || strings.HasSuffix(key, "_token_list")
}

func blockedFlagKey(key string) bool {
	for _, blocked := range []string{"secret", "credential", "header", "endpoint", "host", "profile", "browser", "config", "proxy", "debug", "verbose", "stdin"} {
		if strings.Contains(key, blocked) {
			return true
		}
	}
	return localPathFlag(key)
}

func validResourceValue(value string, list bool) bool {
	parts := []string{value}
	if list {
		parts = strings.Split(value, ",")
	}
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" || len(token) > 4096 || token == "-" || strings.HasPrefix(token, "@") || strings.Contains(token, "://") {
			return false
		}
		for _, r := range token {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}

func resourceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Port() != "" {
		return errors.New("resource URL must be an official HTTPS Feishu/Lark URL")
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range []string{"feishu.cn", "larksuite.com", "feishu.net", "meegle.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return nil
		}
	}
	return errors.New("resource URL host is outside the supported Feishu/Lark domains")
}

func (p *Plugin) status(ctx context.Context, raw json.RawMessage) Result {
	in := struct {
		Verify bool `json:"verify"`
	}{Verify: true}
	if json.Unmarshal(raw, &in) != nil {
		return failure("", "INVALID_ARGUMENTS", "Invalid status options", "Use verify=true or false")
	}
	services := map[string]any{}
	for _, service := range []string{"lark", "project"} {
		path := p.Config.LarkCLI
		if service == "project" {
			path = p.Config.ProjectCLI
		}
		if path == "" {
			services[service] = map[string]any{"installed": false, "authenticated": false, "next_action": "Install/configure the native binary manually; no installer was run"}
			continue
		}
		args := []string{"auth", "status", "--json"}
		if service == "lark" {
			if in.Verify {
				args = append(args, "--verify")
			}
		} else {
			args = []string{"auth", "status", "--format=json"}
		}
		out, err := p.Runner.Run(ctx, service, args, nil)
		var payload map[string]any
		_ = json.Unmarshal(out.Stdout, &payload)
		entry := map[string]any{"installed": err == nil, "authenticated": false, "details": sanitize(payload)}
		if err != nil {
			entry["error"] = err.Error()
		} else if service == "project" {
			entry["authenticated"] = out.Code == 0 && payload["authenticated"] == true
			if out.Code == 2 {
				entry["error"] = "Network/server unavailable; do not reauthenticate solely for this failure"
			}
		} else {
			entry["configured"] = payload["appId"] != nil
			entry["authenticated"] = out.Code == 0 && payload["identity"] == "user" && (!in.Verify || payload["verified"] == true)
			if in.Verify && entry["authenticated"] == true {
				if missing, ok := p.missingUserScopes(ctx); ok {
					entry["missing_user_scopes"] = missing
				}
			}
		}
		services[service] = entry
	}
	return Result{OK: true, Service: "lark+project", Identity: "user", Data: map[string]any{"services": services}, ExecutionState: "completed"}
}

// missingUserScopes compares only explicitly identified app user scopes with
// the current token. Failures are unknown, never evidence of missing grants.
func (p *Plugin) missingUserScopes(ctx context.Context) ([]string, bool) {
	out, err := p.Runner.Run(ctx, "lark", []string{"auth", "scopes", "--json"}, nil)
	var app struct {
		UserScopes []string `json:"userScopes"`
	}
	if err != nil || out.Code != 0 || out.Truncated || json.Unmarshal(out.Stdout, &app) != nil || app.UserScopes == nil || len(app.UserScopes) > 512 {
		return nil, false
	}
	if len(app.UserScopes) == 0 {
		return []string{}, true
	}
	allowed := make(map[string]bool, len(app.UserScopes))
	for _, scope := range app.UserScopes {
		if !diagnosticScope.MatchString(scope) {
			return nil, false
		}
		allowed[scope] = true
	}
	out, err = p.Runner.Run(ctx, "lark", []string{"auth", "check", "--scope", strings.Join(app.UserScopes, " "), "--json"}, nil)
	var check struct {
		OK      *bool    `json:"ok"`
		Missing []string `json:"missing"`
		Error   string   `json:"error"`
	}
	if err != nil || out.Truncated || (out.Code != 0 && out.Code != 1) || json.Unmarshal(out.Stdout, &check) != nil || check.OK == nil || check.Error != "" || (*check.OK != (len(check.Missing) == 0)) {
		return nil, false
	}
	for _, scope := range check.Missing {
		if !allowed[scope] {
			return nil, false
		}
	}
	return check.Missing, true
}
