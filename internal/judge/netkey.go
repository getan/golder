package judge

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/shellfacts"
)

// NetworkKey names the standing scope a remembered network answer would cover.
// It is deliberately narrower than "the call": a host for webfetch, the tool
// for websearch (whose backend host depends on credentials), and one literal
// program/subcommand for bash. Anything the parser cannot pin down returns
// ok=false, and the answer can only be for this call.
func NetworkKey(tool string, args json.RawMessage) (kind grants.NetworkKind, key string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "webfetch":
		var a struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(args, &a) != nil {
			return "", "", false
		}
		u, err := url.Parse(strings.TrimSpace(a.URL))
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		h, err := grants.NormalizeNetworkHost(u.Hostname())
		if err != nil {
			// A local or metadata host may still be fetched once, but it must
			// never become a standing permission.
			return "", "", false
		}
		return grants.NetworkHost, h, true
	case "websearch":
		return grants.NetworkTool, "websearch", true
	case "bash":
		cmd, ok := bashCommand(args)
		if !ok {
			return "", "", false
		}
		plan := shellfacts.Parse(cmd)
		if plan == nil || !plan.Parsed || len(plan.Constructs) > 0 {
			return "", "", false
		}
		// A compound command is still one network question when every part
		// either names the same rememberable program or is a program that
		// cannot reach the network at all. `cd /tmp && GOCACHE=… go test | tail`
		// is one `go test` grant; `git push && curl evil` is not a `git push`
		// grant, and a second rememberable program (go test && git push) would
		// make one rule cover two different questions.
		key := ""
		for _, c := range plan.AllCommands() {
			prog, literal := c.Program()
			if !literal {
				return "", "", false
			}
			words := make([]string, 0, len(c.Argv))
			for _, w := range c.Argv {
				if w.Dynamic {
					return "", "", false
				}
				words = append(words, w.Text)
			}
			k, err := grants.NormalizeNetworkCommand(strings.Join(words, " "))
			if err == nil {
				if key != "" && key != k {
					return "", "", false
				}
				key = k
				continue
			}
			if !nonNetworkPrograms[filepath.Base(strings.TrimSpace(prog))] {
				return "", "", false
			}
		}
		if key == "" {
			return "", "", false
		}
		return grants.NetworkCommand, key, true
	}
	return "", "", false
}

// nonNetworkPrograms are the commands a compound line may contain without
// making its single network rule a lie. The list is an allowlist because its
// failure mode is refusing to remember (the prompt stays), and it deliberately
// excludes every program that can run another command (`find -exec`, `awk
// system()`, `sed e`, `rg --pre`, `xargs`, `env`, the shells and the
// interpreters): a hidden command is a network request this key did not see.
var nonNetworkPrograms = map[string]bool{
	"cd": true, "pwd": true, "echo": true, "printf": true, "true": true, "false": true,
	"test": true, "[": true, "sleep": true, "wait": true, "export": true, "unset": true,
	"set": true, "shift": true, "read": true, "mkdir": true, "rmdir": true, "rm": true,
	"cp": true, "mv": true, "ln": true, "touch": true, "chmod": true, "chown": true,
	"stat": true, "file": true, "ls": true, "cat": true, "head": true, "tail": true,
	"grep": true, "sort": true, "uniq": true, "wc": true, "cut": true, "tr": true,
	"tee": true, "basename": true, "dirname": true, "realpath": true, "readlink": true,
	"date": true, "which": true, "type": true, "hash": true, "umask": true,
}

// NetworkRemembered returns the standing answer for this call, if one exists.
func NetworkRemembered(tool string, args json.RawMessage) (grants.NetworkVerdict, bool) {
	kind, key, ok := NetworkKey(tool, args)
	if !ok {
		return grants.NetworkVerdict{}, false
	}
	v := grants.NetworkCheck(kind, key, tool)
	return v, v.Decision != grants.NetworkUndecided
}
