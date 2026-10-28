// Package plugin provides a Go server for golder's existing stdio plugin v1
// protocol. New cross-host tool integrations should generally use MCP instead.
package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/getan/golder/extension"
)

type Tool struct {
	Spec   extension.ToolSpec
	Handle extension.ToolHandler
}

type Notification struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}

type CommandResult struct {
	Prompt        string         `json:"prompt,omitempty"`
	Notifications []Notification `json:"notifications,omitempty"`
}

type Command struct {
	Name, Description string
	Handle            func(context.Context, string) (CommandResult, error)
}

type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type Spec struct {
	APIVersion    string // empty or "1"; this SDK deliberately keeps the v1 wire
	ID, Version   string
	Tools         []Tool
	Commands      []Command
	Subscriptions []string
	OnEvent       func(context.Context, Event)
}

func Serve(ctx context.Context, spec Spec) error {
	return ServeIO(ctx, spec, os.Stdin, os.Stdout)
}

// ServeIO serves until EOF, shutdown or cancellation. The caller owns in and
// must close it to interrupt a blocked read. Handlers must observe ctx.
// Calls are serialized, matching legacy plugin execution semantics.
func ServeIO(ctx context.Context, spec Spec, in io.Reader, out io.Writer) error {
	if spec.ID == "" || (spec.APIVersion != "" && spec.APIVersion != "1") {
		return errors.New("plugin: id and supported API version are required")
	}
	tools := make(map[string]Tool)
	for _, t := range spec.Tools {
		if t.Spec.Name == "" || t.Handle == nil || tools[t.Spec.Name].Handle != nil {
			return errors.New("plugin: invalid or duplicate tool")
		}
		tools[t.Spec.Name] = t
	}
	commands := make(map[string]Command)
	for _, c := range spec.Commands {
		if c.Name == "" || c.Handle == nil || commands[c.Name].Handle != nil {
			return errors.New("plugin: invalid or duplicate command")
		}
		commands[c.Name] = c
	}
	var mu sync.Mutex
	enc := json.NewEncoder(out)
	reply := func(id json.RawMessage, result any, err error) error {
		m := map[string]any{"jsonrpc": "2.0", "id": id}
		if err != nil {
			m["error"] = map[string]any{"code": -32603, "message": err.Error()}
		} else {
			m["result"] = result
		}
		mu.Lock()
		defer mu.Unlock()
		return enc.Encode(m)
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req struct {
			Version string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return fmt.Errorf("plugin: malformed request: %w", err)
		}
		if req.Version != "2.0" {
			return errors.New("plugin: expected JSON-RPC 2.0")
		}
		if req.Method == "shutdown" {
			return nil
		}
		if req.Method == "event" && len(req.ID) == 0 {
			var ev Event
			if json.Unmarshal(req.Params, &ev) == nil && spec.OnEvent != nil {
				for _, kind := range spec.Subscriptions {
					if kind == ev.Type {
						spec.OnEvent(ctx, ev)
						break
					}
				}
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		var result any
		var callErr error
		switch req.Method {
		case "initialize":
			var ts []map[string]any
			for _, t := range spec.Tools {
				ts = append(ts, map[string]any{"name": t.Spec.Name, "description": t.Spec.Description, "schema": t.Spec.InputSchema})
			}
			var cs []map[string]any
			for _, c := range spec.Commands {
				cs = append(cs, map[string]any{"name": c.Name, "description": c.Description})
			}
			result = map[string]any{"name": spec.ID, "version": spec.Version, "tools": ts, "commands": cs, "events": spec.Subscriptions}
		case "tools/call", "commands/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				callErr = err
				break
			}
			if req.Method == "commands/call" {
				c, ok := commands[p.Name]
				if !ok {
					callErr = fmt.Errorf("unknown command %q", p.Name)
					break
				}
				var arg string
				if err := json.Unmarshal(p.Arguments, &arg); err != nil {
					callErr = err
					break
				}
				result, callErr = c.Handle(ctx, arg)
			} else {
				t, ok := tools[p.Name]
				if !ok {
					callErr = fmt.Errorf("unknown tool %q", p.Name)
					break
				}
				r, err := t.Handle(ctx, extension.Call{ID: string(req.ID), Tool: p.Name, Arguments: p.Arguments}, func(extension.Progress) {})
				callErr = err
				var parts []string
				for _, b := range r.Content {
					if b.Type == "text" {
						parts = append(parts, b.Text)
					} else {
						parts = append(parts, "["+b.Type+"]")
					}
				}
				if len(r.StructuredContent) > 0 {
					parts = append(parts, string(r.StructuredContent))
				}
				result = map[string]any{"content": strings.Join(parts, "\n"), "isError": r.IsError}
			}
		default:
			callErr = fmt.Errorf("unsupported method %q", req.Method)
		}
		if err := reply(req.ID, result, callErr); err != nil {
			return err
		}
	}
	return scanner.Err()
}
