//go:build windows

package mygoui

import "os"

// Preserve Windows' Mark of the Web so the installer receives normal security
// checks when the person explicitly opens it.
func markGUIInternetDownload(path, source string) error {
	return os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\nHostUrl="+source+"\r\n"), 0o600)
}
