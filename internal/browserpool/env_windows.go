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
	allowed := map[string]bool{
		"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true,
		"USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true, "PROGRAMDATA": true,
		"PROGRAMFILES": true, "PROGRAMFILES(X86)": true, "COMMONPROGRAMFILES": true,
		"NUMBER_OF_PROCESSORS": true, "PROCESSOR_ARCHITECTURE": true, "PROCESSOR_IDENTIFIER": true,
		"OS": true, "USERNAME": true, "USERDOMAIN": true, "PUBLIC": true, "TZ": true,
	}
	out := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if allowed[strings.ToUpper(name)] {
			out = append(out, entry)
		}
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
