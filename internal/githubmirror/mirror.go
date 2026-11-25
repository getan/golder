// Package githubmirror centralizes the GitHub download accelerator list shared
// by install.sh, self-update and the first-party extension downloaders.
package githubmirror

import (
	"os"
	"strings"
)

// Env overrides the built-in mirror list: whitespace-separated URL prefixes,
// or "off"/"none" to disable mirror fallback.
const Env = "GOLDER_MIRROR"

// Defaults are community-run GitHub download accelerators. A mirror URL is the
// original GitHub URL prefixed by the accelerator (the gh-proxy convention).
// They are transport helpers only; every downloaded archive is still verified
// against a trusted checksum.
var Defaults = []string{
	"https://ghfast.top/",
	"https://ghproxy.net/",
	"https://gh-proxy.com/",
	"https://gh.zwy.one/",
}

// Prefixes resolves the mirror list: GOLDER_MIRROR when set, else Defaults.
func Prefixes() []string {
	raw := strings.TrimSpace(os.Getenv(Env))
	switch raw {
	case "":
		return append([]string(nil), Defaults...)
	case "off", "none":
		return nil
	default:
		return strings.Fields(raw)
	}
}

// Apply returns the direct URL followed by one URL for each mirror prefix.
// Only GitHub release URLs are mirrored; other HTTPS resources are returned
// unchanged.
func Apply(original string, prefixes []string) []string {
	out := []string{original}
	if !strings.HasPrefix(original, "https://github.com/") {
		return out
	}
	for _, prefix := range prefixes {
		if prefix = strings.TrimSpace(prefix); prefix != "" {
			out = append(out, prefix+original)
		}
	}
	return out
}
