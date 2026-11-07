package config

import (
	"fmt"

	"github.com/getan/golder/internal/filelock"
)

// lockConfigFile serializes a read-modify-write of config.toml across GUI, TUI
// and CLI processes. The lock is released by the returned function.
func lockConfigFile(path string) (func(), error) {
	unlock, err := filelock.Acquire(path)
	if err != nil {
		return nil, fmt.Errorf("cannot lock the config file: %w", err)
	}
	return unlock, nil
}
