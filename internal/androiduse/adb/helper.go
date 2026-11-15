package adb

import (
	"fmt"
	"strings"
)

// HelperLaunchCommand constructs the shell command used after the ADB
// connection is authenticated. The nonce is not part of this string; it is
// written to the exec stream's stdin so it does not appear in /proc cmdline.
func HelperLaunchCommand(apkPath, packageName, helperClass, certPin string, appUID int) string {
	processName := packageName + ":helper"
	return "read n; (echo \"$n\" | CLASSPATH=" + shellQuote(apkPath) +
		" setsid app_process /system/bin --nice-name=" + shellQuote(processName) +
		" " + shellQuote(helperClass) +
		" " + shellQuote(packageName) +
		fmt.Sprintf(" %d", appUID) +
		" " + shellQuote(certPin) +
		" >/dev/null 2>&1 &); read _"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func HelperStdin(nonce string) ([]byte, error) {
	if nonce == "" || strings.ContainsAny(nonce, "\r\n") {
		return nil, fmt.Errorf("invalid helper nonce")
	}
	return []byte(nonce + "\n"), nil
}

// LaunchHelper starts the helper through a paired ADB connection. The nonce is
// written to the exec stream's stdin, never to the helper command line.
func LaunchHelper(conn *Conn, apkPath, packageName, helperClass, certPin string, appUID int, nonce string) error {
	if conn == nil {
		return fmt.Errorf("ADB connection is required")
	}
	stdin, err := HelperStdin(nonce)
	if err != nil {
		return err
	}
	stream, err := conn.Open("exec:" + HelperLaunchCommand(apkPath, packageName, helperClass, certPin, appUID))
	if err != nil {
		return err
	}
	if err := stream.Write(stdin); err != nil {
		_ = stream.Close()
		return err
	}
	return stream.Close()
}
