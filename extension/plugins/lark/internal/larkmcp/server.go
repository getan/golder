package larkmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/extension"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Plugin struct {
	Config Config
	Runner Runner
	mu     sync.Mutex
	defs   map[string]definition
	wait   func(context.Context, time.Duration) error
}

func New(config Config, runner Runner) *Plugin {
	if runner == nil {
		runner = NativeRunner{Config: config}
	}
	p := &Plugin{Config: config, Runner: runner, defs: map[string]definition{}, wait: waitPoll}
	for _, d := range definitions() {
		p.defs[d.Name] = d
	}
	return p
}

func (p *Plugin) Server(version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "golder-lark", Version: version}, nil)
	add := func(name, description string, schema json.RawMessage) {
		s.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if request.Params.Meta[extension.OperatorConfirmedMeta] == true {
				ctx = context.WithValue(ctx, operatorKey{}, true)
			}
			result := p.Call(ctx, name, request.Params.Arguments)
			b, _ := json.Marshal(result)
			return &mcp.CallToolResult{IsError: !result.OK, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
		})
	}
	names := make([]string, 0, len(p.defs))
	for name := range p.defs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d := p.defs[name]
		add(name, d.Description, d.schema())
	}
	add("status", "Diagnose Lark and Project separately: install/configuration/user authorization, not MCP readiness. Explicit call may verify the server; never auto-installs or logs in.", json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"verify":{"type":"boolean","default":true}}}`))
	add("cli_help", "Read bounded help for one Lark/Project business command. Use before cli_call for long-tail operations; no full CLI manual in context.", tailSchema(false))
	add("cli_call", "Execute an approved long-tail business command in the same private state/sandbox. Native fixed CLI only; no shell, auth/config/install, raw API or local-file inputs. Requires exact operator approval.", tailSchema(true))
	return s
}

type input struct {
	Operation string         `json:"operation"`
	Options   map[string]any `json:"options"`
	Confirm   bool           `json:"confirm"`
}

type operatorKey struct{}

func operatorConfirmed(ctx context.Context) bool { v, _ := ctx.Value(operatorKey{}).(bool); return v }

func waitPoll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *Plugin) Call(ctx context.Context, name string, raw json.RawMessage) Result {
	p.mu.Lock()
	defer p.mu.Unlock() // token writes / same-table operations are serialized
	if name == "status" {
		return p.status(ctx, raw)
	}
	if name == "cli_help" || name == "cli_call" {
		return p.tail(ctx, name, raw)
	}
	d, ok := p.defs[name]
	if !ok {
		return failure("", "UNKNOWN_TOOL", "Unknown Lark MCP tool", "Refresh the catalog")
	}
	if d.Write && !operatorConfirmed(ctx) {
		return failure(d.Service, "OPERATOR_CONFIRMATION_REQUIRED", "This write requires the host's exact human approval, not a model boolean", "Use the supplied confirm_tools configuration in ask mode")
	}
	var in input
	if json.Unmarshal(raw, &in) != nil {
		return failure(d.Service, "INVALID_ARGUMENTS", "Expected an object with operation/options", "Use the tool schema")
	}
	if in.Operation == "" {
		in.Operation = d.Default
	}
	operation, ok := d.Operations[in.Operation]
	if !ok {
		return failure(d.Service, "INVALID_OPERATION", "Unsupported operation", "Use a declared operation")
	}
	if d.Write && !in.Confirm {
		return failure(d.Service, "INTENT_REQUIRED", "Confirm the exact target and change before requesting execution", "confirm=true is intent, not host authorization")
	}
	for key := range in.Options {
		if _, ok := d.Options[key]; !ok {
			return failure(d.Service, "INVALID_ARGUMENTS", "Unknown option: "+key, "Use cli_help for long-tail capabilities")
		}
	}
	for _, key := range operation.Required {
		if value, present := in.Options[key]; !present || value == nil || value == "" {
			return failure(d.Service, "MISSING_ARGUMENT", key+" is required", "Resolve the exact target and required parameters")
		}
	}
	if err := validateBusiness(d.Name, in); err != nil {
		return failure(d.Service, "INVALID_ARGUMENTS", err.Error(), "Read the corresponding Lark Skill reference or cli_help")
	}
	if d.Service == "project" {
		return p.project(ctx, d, in, operation)
	}
	args, err := buildArgs(operation.Command, in.Options)
	if err != nil {
		return failure(d.Service, "INVALID_ARGUMENTS", err.Error(), "Only inline payloads are allowed")
	}
	if name == "doc_write" {
		args = append(args, "--doc-format=markdown")
		commands := map[string]string{"append": "append", "replace_text": "str_replace", "replace_block": "block_replace"}
		if command := commands[in.Operation]; command != "" {
			args = append(args, "--command="+command)
		}
	}
	return p.lark(ctx, d, in, args)
}

func (p *Plugin) run(ctx context.Context, service string, args []string, write bool) Result {
	args = append([]string{}, args...)
	if service == "lark" {
		args = append(args, "--as=user")
	} else {
		args = append(args, "--format=json")
	}
	out, err := p.Runner.Run(ctx, service, args, nil)
	r := normalize(service, out, err, write)
	if r.OK && write {
		r.ExecutionState, r.Verification = "submitted", "receipt_only"
	}
	return r
}

func buildArgs(command []string, opts map[string]any) ([]string, error) {
	args := append([]string{}, command...)
	keys := make([]string, 0, len(opts))
	for key := range opts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := opts[key]
		var text string
		if s, ok := value.(string); ok {
			text = s
		} else {
			b, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			text = string(b)
		}
		if strings.HasPrefix(strings.TrimSpace(text), "@") || text == "-" || strings.ContainsRune(text, 0) {
			return nil, errors.New("file/stdin references are not allowed")
		}
		args = append(args, "--"+strings.ReplaceAll(key, "_", "-")+"="+text)
	}
	return args, nil
}

func validateBusiness(name string, in input) error {
	o := in.Options
	if strings.HasPrefix(name, "sheet_") {
		if (o["url"] == nil) == (o["spreadsheet_token"] == nil) {
			return errors.New("provide exactly one of url or spreadsheet_token")
		}
		if (name == "sheet_read" && in.Operation != "workbook" || name == "sheet_write" && in.Operation == "cells" || name == "sheet_structure" && in.Operation == "insert") && o["sheet_id"] == nil && o["sheet_name"] == nil {
			return errors.New("resolve a real sheet_id or sheet_name first")
		}
	}
	if name == "message_send" || name == "chat_history" {
		if (o["chat_id"] == nil) == (o["user_id"] == nil) {
			return errors.New("provide exactly one chat_id or user_id")
		}
		if name == "message_send" && (o["text"] == nil) == (o["markdown"] == nil) {
			return errors.New("provide exactly one text or markdown body")
		}
	}
	if name == "doc_search" {
		if query, _ := o["query"].(string); len([]rune(query)) > 30 {
			return errors.New("Drive search accepts at most 30 Unicode characters; shorten the query")
		}
	}
	for key, value := range o {
		if s, ok := value.(string); ok && (strings.Contains(key, "url") || key == "doc") && strings.Contains(s, "://") {
			if err := resourceURL(s); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	return nil
}
