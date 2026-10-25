// Package controlcap describes the effects of the terminal and browser facades.
// It is shared by review, consent and execution; changing a tool's name must not
// change the protections of the operation it performs.
package controlcap

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/kballard/go-shellquote"
)

type Request struct {
	Action      string   `json:"action"`
	Command     string   `json:"command"`
	Text        string   `json:"text"`
	Argv        []string `json:"argv"`
	Cwd         string   `json:"cwd"`
	Backend     string   `json:"backend"`
	Frontend    string   `json:"frontend"`
	Preset      string   `json:"preset"`
	URL         string   `json:"url"`
	Network     string   `json:"network"`
	HostControl bool     `json:"host_control"`
}

func Facade(tool string) bool {
	return tool == "tuios" || tool == "workspace" || tool == "browser"
}

func Parse(raw json.RawMessage) Request {
	var r Request
	_ = json.Unmarshal(raw, &r)
	return r
}

// Commands preserves argv boundaries, including a shell's entire -c argument.
// send is potential shell input, not a harmless string write.
func Commands(tool string, raw json.RawMessage) []string {
	r := Parse(raw)
	var cmds []string
	switch tool {
	case "tuios":
		switch r.Action {
		case "run":
			cmds = append(cmds, r.Command)
		case "send":
			cmds = append(cmds, r.Text)
		case "new":
			if len(r.Argv) > 0 {
				cmds = append(cmds, shellquote.Join(r.Argv...))
			}
		}
	case "workspace":
		if r.Action == "create" {
			cmds = append(cmds, r.Backend, r.Frontend)
		}
	}
	var out []string
	for _, cmd := range cmds {
		if strings.TrimSpace(cmd) != "" {
			out = append(out, cmd)
		}
	}
	return out
}

// ReadOnly excludes screenshot writes and all launch/input/control actions.
func ReadOnly(tool string, raw json.RawMessage) bool {
	r := Parse(raw)
	switch tool {
	case "tuios":
		return r.Action == "list" || r.Action == "capture" || r.Action == "wait"
	case "workspace":
		return r.Action == "list" || r.Action == "snapshot"
	case "browser":
		switch r.Action {
		case "list", "snapshot", "find", "get", "is", "wait", "dialog", "console", "errors", "network":
			return true
		}
	}
	return false
}

func Network(tool string, raw json.RawMessage) bool {
	r := Parse(raw)
	if tool == "tuios" {
		return r.Network == "on" || (r.HostControl && (r.Action == "run" || r.Action == "send" || r.Action == "press"))
	}
	if tool == "workspace" {
		return r.Action == "create" && (r.Network == "on" || r.Preset == "debug-fullstack" || r.Preset == "review")
	}
	if tool == "browser" {
		switch r.Action {
		case "list", "attach", "snapshot", "find", "get", "is", "wait", "dialog", "console", "errors", "network", "screenshot":
			return false
		default:
			return true // page input can submit forms and trigger requests
		}
	}
	return false
}

// BrowserURL is used on every URL-bearing entry, not only navigate. Local file,
// script, browser-internal and opaque schemes must never reach a browser broker.
func BrowserURL(raw string) error {
	if raw == "" || raw == "about:blank" {
		return nil
	}
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("browser URL must not contain surrounding whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("browser URLs must be absolute http(s) URLs without credentials (or about:blank)")
	}
	return nil
}
