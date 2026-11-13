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

var commandName = regexp.MustCompile(`^[+]?[a-z][a-z0-9-]*$`)

func tailSchema(execute bool) json.RawMessage {
	key := "command"
	if execute {
		key = "argv"
	}
	props := map[string]any{"service": map[string]any{"type": "string", "enum": []string{"lark", "project"}}, key: map[string]any{
		"type": "array", "minItems": 1, "maxItems": 64,
		"description": "Inline business arguments. Omit --as, --format, --yes, --force and --no-confirm; the adapter controls identity, output format and confirmation.",
		"items":       map[string]any{"type": "string", "maxLength": 16000},
	}}
	required := []string{"service", key}
	if execute {
		props["confirm"] = map[string]any{"type": "boolean", "const": true}
		required = append(required, "confirm")
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required})
	return b
}

func (p *Plugin) tail(ctx context.Context, name string, raw json.RawMessage) Result {
	var in struct {
		Service string   `json:"service"`
		Command []string `json:"command"`
		Argv    []string `json:"argv"`
		Confirm bool     `json:"confirm"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return failure("", "INVALID_ARGUMENTS", "Invalid long-tail request", "Use cli_help first")
	}
	if name == "cli_help" {
		if err := businessCommand(in.Service, in.Command, true); err != nil {
			if in.Service == "project" && len(in.Command) > 0 && (in.Command[0] == "ticket" || in.Command[0] == "ticket_search" || in.Command[0] == "ticket-search") {
				return failure(in.Service, "COMMAND_BLOCKED", "ticket_search is an MCP tool, not a Project CLI command family", `For work-item query help call cli_help with {"service":"project","command":["workitem","query"]}; command availability depends on the installed CLI and Project login`)
			}
			return failure(in.Service, "COMMAND_BLOCKED", err.Error(), "Only business command help is available")
		}
		args := append(append([]string{}, in.Command...), "--help")
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
	if err := businessCommand(in.Service, in.Argv[:2], false); err != nil {
		return failure(in.Service, "COMMAND_BLOCKED", err.Error(), "Use setup for authorization; management/raw API commands are not exposed")
	}
	if err := businessFlags(in.Service, in.Argv[:2], in.Argv[2:]); err != nil {
		return failure(in.Service, "ARGUMENT_BLOCKED", err.Error(), "Omit --as/--format/confirmation flags; use only approved inline business parameters, not local-file or configuration references")
	}
	// This one fixed command is a known read, independent of untrusted CLI
	// annotations. Keep the same exact-call host authorization requirement;
	// all other long-tail commands remain conservatively mutating/unknown.
	knownSearchRead := in.Service == "lark" && in.Argv[0] == "drive" && in.Argv[1] == "+search"
	return p.run(ctx, in.Service, in.Argv, !knownSearchRead)
}

func businessCommand(service string, parts []string, help bool) error {
	if len(parts) == 0 || len(parts) > 2 {
		return errors.New("help accepts only a domain and optional command")
	}
	for _, part := range parts {
		if !commandName.MatchString(part) {
			return errors.New("invalid command path")
		}
	}
	root := parts[0]
	allowed := " docs drive im sheets base "
	if service == "project" {
		allowed = " workitem workflow project user mywork view comment workhour url "
	} else if service != "lark" {
		return errors.New("unknown service")
	}
	if !strings.Contains(allowed, " "+root+" ") {
		return errors.New("this command family is not exposed")
	}
	if help {
		return nil
	}
	operation := parts[1]
	if service == "lark" && !strings.HasPrefix(operation, "+") {
		return errors.New("only business shortcuts are exposed, not raw API subcommands")
	}
	if service == "project" && (root == "project" || root == "user") && operation != "search" && operation != "me" {
		return errors.New("project/user management is not exposed")
	}
	for _, word := range []string{"delete", "remove", "trash", "clear", "rollback", "revert", "restore", "permission", "advperm", "share", "owner", "upload", "download", "export", "import", "automation", "webhook", "workflow", "script"} {
		if strings.Contains(operation, word) {
			return errors.New("destructive, permission, file-transfer or automation commands require a dedicated reviewed tool")
		}
	}
	if service == "project" && root == "workflow" && (operation == "create" || operation == "enable" || operation == "disable") {
		return errors.New("workflow configuration is not exposed")
	}
	return nil
}

// larkResourceFlags lists the only long-tail resource identifiers reviewed for
// each business command. Do not broaden this to every flag containing "token":
// the point is to let business identifiers through without also exposing
// credentials, local files or unreviewed capabilities.
var larkResourceFlags = map[string]map[string]bool{
	"drive +copy":             {"folder-token": true, "token": true},
	"drive +create-folder":    {"folder-token": true},
	"sheets +workbook-create": {"folder-token": true},
	"drive +search":           {"folder-tokens": true},
	"drive +move":             {"file-token": true, "folder-token": true},
	"drive +create-shortcut":  {"file-token": true, "folder-token": true},
	"docs +create":            {"parent-token": true},
}

type businessFlag struct {
	key, value string
}

func businessFlags(service string, command, args []string) error {
	flags, err := parseBusinessFlags(args)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	if service == "lark" && len(command) == 2 {
		allowed = larkResourceFlags[command[0]+" "+command[1]]
	}
	sawType, typeValue, sawToken := false, "", false
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
		if key == "type" {
			sawType, typeValue = true, value
		}
		if key == "token" {
			sawToken = true
		}
		if allowed[key] || existingResourceFlag(key) {
			if value == "" {
				return fmt.Errorf("--%s requires an inline resource token", key)
			}
			if !validResourceValue(value, key == "folder-tokens") {
				return fmt.Errorf("--%s must be an inline resource token, not a local file, URL or stdin reference", key)
			}
			continue
		}
		if strings.Contains(key, "token") {
			return fmt.Errorf("--%s is not an approved resource parameter for %s %s", key, service, strings.Join(command, " "))
		}
		if blockedFlagKey(key) {
			return fmt.Errorf("--%s is a configuration, credential, or local-file flag and is not exposed", key)
		}
		if strings.HasPrefix(strings.TrimSpace(value), "@") || value == "-" || strings.ContainsAny(value, "\x00\r") {
			return errors.New("local-file/stdin references are not allowed")
		}
		if strings.Contains(key, "url") && strings.Contains(value, "://") {
			if err := resourceURL(value); err != nil {
				return err
			}
		}
	}
	if sawToken && (!sawType || typeValue == "") {
		return errors.New("--token requires an inline --type for drive +copy")
	}
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

func existingResourceFlag(key string) bool {
	return key == "base-token" || key == "spreadsheet-token" || key == "page-token"
}

func blockedFlagKey(key string) bool {
	for _, blocked := range []string{"file", "path", "output", "token", "secret", "credential", "header", "endpoint", "host", "profile", "browser", "config", "proxy", "debug", "verbose", "stdin", "image", "audio", "video", "attachment", "media"} {
		if strings.Contains(key, blocked) {
			return true
		}
	}
	return false
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
