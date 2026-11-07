package selfupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/privatepath"
)

// Only stable release tags can become download paths or saved preferences.
var stableTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type updatePreferences struct {
	SkippedVersion string `json:"skipped_version"`
}

// StartupOffer reports whether latest is a newer, unskipped stable release.
func StartupOffer(current, latest string) bool {
	if !stableTag.MatchString(latest) {
		return false
	}
	available, comparable := UpdateAvailable(current, latest)
	if !comparable || !available {
		return false
	}
	path := golderhome.File(golderhome.UpdatePrefsFile)
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 64<<10 {
		return true
	}
	var prefs updatePreferences
	if json.Unmarshal(data, &prefs) != nil || !stableTag.MatchString(prefs.SkippedVersion) {
		return true
	}
	// A stale check must not re-offer an older version after a newer skip.
	newer, comparable := UpdateAvailable(prefs.SkippedVersion, latest)
	return !comparable || newer
}

// SkipVersion atomically records the exact offered version for later startups.
func SkipVersion(tag string) error {
	if !stableTag.MatchString(tag) {
		return errors.New("invalid release version")
	}
	path := golderhome.File(golderhome.UpdatePrefsFile)
	if path == "" {
		return errors.New("Golder state root unavailable")
	}
	if err := privatepath.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(updatePreferences{SkippedVersion: tag})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".update-preferences-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
