package larkmcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var commandName = regexp.MustCompile(`^[+]?[a-z][a-z0-9-]*$`)

func tailSchema(execute bool) json.RawMessage {
	key := "command"
	if execute {
		key = "argv"
	}
	props := map[string]any{"service": map[string]any{"type": "string", "enum": []string{"lark", "project"}}, key: map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": map[string]any{"type": "string", "maxLength": 16000}}}
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
			return failure(in.Service, "COMMAND_BLOCKED", err.Error(), "Only business command help is available")
		}
		args := append(append([]string{}, in.Command...), "--help")
		out, err := p.Runner.Run(ctx, in.Service, args, nil)
		return normalize(in.Service, out, err, false)
	}
	if !in.Confirm {
		return failure(in.Service, "INTENT_REQUIRED", "Confirm the exact CLI operation before execution", "Host approval is separate")
	}
	if !operatorConfirmed(ctx) {
		return failure(in.Service, "OPERATOR_CONFIRMATION_REQUIRED", "Long-tail execution requires the host's exact human approval", "Use the supplied confirm_tools configuration in ask mode")
	}
	if len(in.Argv) < 2 {
		return failure(in.Service, "COMMAND_BLOCKED", "Provide a business command and its operation", "Use cli_help")
	}
	if err := businessCommand(in.Service, in.Argv[:2], false); err != nil {
		return failure(in.Service, "COMMAND_BLOCKED", err.Error(), "Use setup for authorization; management/raw API commands are not exposed")
	}
	if err := businessFlags(in.Argv[2:]); err != nil {
		return failure(in.Service, "ARGUMENT_BLOCKED", err.Error(), "Use only inline business parameters, not local-file or configuration references")
	}
	return p.run(ctx, in.Service, in.Argv, true)
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

func businessFlags(args []string) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || a == "--" {
			return errors.New("only explicit long flags are accepted")
		}
		key, value, equals := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !commandName.MatchString(key) {
			return errors.New("invalid flag name")
		}
		resourceToken := key == "base-token" || key == "spreadsheet-token" || key == "page-token"
		if !resourceToken {
			for _, blocked := range []string{"file", "path", "output", "token", "secret", "credential", "header", "endpoint", "host", "profile", "browser", "config", "proxy", "debug", "verbose", "stdin", "image", "audio", "video", "attachment", "media"} {
				if strings.Contains(key, blocked) {
					return errors.New("configuration/credential/file flags are not exposed")
				}
			}
		}
		if key == "as" || key == "yes" || key == "force" || key == "no-confirm" || key == "format" {
			return errors.New("identity, format and confirmation are controlled by the adapter")
		}
		if !equals && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
			value = args[i]
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
	return nil
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
		}
		services[service] = entry
	}
	return Result{OK: true, Service: "lark+project", Identity: "user", Data: map[string]any{"services": services}, ExecutionState: "completed"}
}
