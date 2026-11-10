//go:build darwin

package mygoui

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// Preserve macOS Gatekeeper's normal treatment of an internet download.
func markGUIInternetDownload(path, _ string) error {
	value := fmt.Sprintf("0081;%x;Golder GUI;", time.Now().Unix())
	return unix.Setxattr(path, "com.apple.quarantine", []byte(value), 0)
}
