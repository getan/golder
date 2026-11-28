//go:build !windows

package browserpool

import (
	"os"
	"path/filepath"
)

func browserHelperEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"CFFIXED_USER_HOME=" + home,
		"TMPDIR=" + os.TempDir(),
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
	}
}
