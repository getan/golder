//go:build windows

package browserpool

import (
	"os"
	"path/filepath"
	"strings"
)

func browserHelperEnv(home string) []string {
	tmp := filepath.Join(home, "tmp")
	_ = os.MkdirAll(tmp, 0o700)
	out := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(name) {
		case "HOME", "TMPDIR", "TEMP", "TMP", "XDG_CONFIG_HOME", "XDG_CACHE_HOME":
			continue
		}
		out = append(out, entry)
	}
	return append(out,
		"HOME="+home,
		"TMPDIR="+tmp,
		"TEMP="+tmp,
		"TMP="+tmp,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
	)
}
