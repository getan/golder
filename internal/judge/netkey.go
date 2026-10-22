package judge

import (
	"encoding/json"
	"net/url"
	"strings"
)

// NetworkKey names the host a network need is about, for the dialog and for a
// one-call answer. The network is decided per call now: there is no standing
// host/command rule to match, so a key this layer cannot pin down is simply
// empty and the dialog says "network access".
func NetworkKey(tool string, args json.RawMessage) (key string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "webfetch":
		var a struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(args, &a) != nil {
			return "", false
		}
		u, err := url.Parse(strings.TrimSpace(a.URL))
		if err != nil || u.Hostname() == "" {
			return "", false
		}
		return strings.ToLower(u.Hostname()), true
	case "websearch":
		return "websearch", true
	}
	return "", false
}
