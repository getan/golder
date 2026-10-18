package judge

import (
	"encoding/json"
	"net/url"
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
		cmds := plan.AllCommands()
		if len(cmds) != 1 {
			return "", "", false
		}
		c := cmds[0]
		if c.Background || c.Negated || c.InSubshell || c.InPipeline ||
			len(c.Assigns) > 0 || len(c.Redirs) > 0 || len(c.Nested) > 0 {
			return "", "", false
		}
		words := make([]string, 0, len(c.Argv))
		for _, w := range c.Argv {
			if w.Dynamic {
				return "", "", false
			}
			words = append(words, w.Text)
		}
		key, err := grants.NormalizeNetworkCommand(strings.Join(words, " "))
		if err != nil {
			return "", "", false
		}
		return grants.NetworkCommand, key, true
	}
	return "", "", false
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
