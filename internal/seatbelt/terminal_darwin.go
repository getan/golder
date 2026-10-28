//go:build darwin

package seatbelt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	selfPTYParameter      = "GOLDER_SELF_PTY"
	selfTTYAliasParameter = "GOLDER_SELF_CTTY_ALIAS"
)

// Only ordinary termios, size, flush and foreground-job operations belong to a
// pane. In particular, not TIOCSTI (input injection), TIOCSETD (line discipline)
// or TIOCCONS (console redirection). Render numeric constants: older macOS
// Seatbelt compilers do not define all the symbolic ioctl names.
func ownTerminalProfile() string {
	var commands []string
	for _, request := range []uint{
		unix.TIOCGETA, unix.TIOCSETA, unix.TIOCSETAW, unix.TIOCSETAF,
		unix.TIOCGETD, unix.TIOCGWINSZ, unix.TIOCSWINSZ,
		unix.TIOCGPGRP, unix.TIOCSPGRP, unix.TIOCFLUSH,
	} {
		commands = append(commands, fmt.Sprintf("(ioctl-command %d)", request))
	}
	return fmt.Sprintf("(allow file-ioctl (require-all (require-any (literal (param %q)) (literal (param %q))) (require-any %s)))\n"+
		"(allow file-read* file-write-data (literal (param %q)))\n",
		selfPTYParameter, selfTTYAliasParameter, strings.Join(commands, " "), selfPTYParameter)
}

// ExecTerminalSandbox runs only in the freshly allocated pane child. The daemon
// has established its controlling slave before this trusted entry point runs;
// no user program has run yet. Never bind the launching agent's terminal or
// accept a device path from tool arguments, environment, or a pane title.
func ExecTerminalSandbox(args []string) error {
	if len(args) < 3 || args[1] != "--" || !filepath.IsAbs(args[0]) {
		return errors.New("invalid terminal sandbox bootstrap arguments")
	}
	path, err := ownPTYPath(os.Stdin)
	if err != nil {
		return fmt.Errorf("bind pane terminal before execution: %w", err)
	}
	alias := "/dev/golder-no-controlling-terminal"
	if path == "" {
		// A redirected (non-TTY) invocation gets no terminal capability. This
		// nonexistent devfs name cannot name another pane or a host terminal.
		path = "/dev/golder-no-controlling-terminal"
	} else {
		alias, err = ownControllingTTYAlias(os.Stdin)
		if err != nil {
			return fmt.Errorf("verify pane controlling terminal before execution: %w", err)
		}
	}
	argv := []string{"sandbox-exec", "-D", selfPTYParameter + "=" + path,
		"-D", selfTTYAliasParameter + "=" + alias, "-f", args[0], "--"}
	argv = append(argv, args[2:]...)
	return unix.Exec("/usr/bin/sandbox-exec", argv, os.Environ())
}

// /dev/tty is a distinct vnode, so getpass needs the same narrow ioctl grant
// on this alias. Mint it only for the fresh session leader whose allocated
// stdin and controlling terminal both identify its foreground process group.
// A borrowed terminal or non-TTY invocation cannot authorize a host terminal.
func ownControllingTTYAlias(file *os.File) (string, error) {
	pid := unix.Getpid()
	sid, err := unix.Getsid(0)
	if err != nil || sid != pid || unix.Getpgrp() != pid {
		return "", errors.New("pane bootstrap is not its own session/process-group leader")
	}
	group, err := unix.IoctlGetInt(int(file.Fd()), unix.TIOCGPGRP)
	if err != nil || group != pid {
		return "", errors.New("allocated pane PTY does not belong to the new foreground process group")
	}
	ctty, err := os.OpenFile("/dev/tty", os.O_RDONLY|unix.O_NOCTTY, 0)
	if err != nil {
		return "", err
	}
	defer ctty.Close()
	group, err = unix.IoctlGetInt(int(ctty.Fd()), unix.TIOCGPGRP)
	if err != nil || group != pid {
		return "", errors.New("controlling terminal does not belong to the allocated pane session")
	}
	return "/dev/tty", nil
}

func ownPTYPath(file *os.File) (string, error) {
	if _, err := unix.IoctlGetTermios(int(file.Fd()), unix.TIOCGETA); err != nil {
		// Darwin reports ENODEV for /dev/null, not ENOTTY. Both identify
		// non-terminal stdio; permission and descriptor errors must still fail.
		if errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.ENODEV) {
			return "", nil
		}
		return "", err
	}
	var buffer [unix.PathMax]byte
	_, _, errno := unix.Syscall(unix.SYS_FCNTL, file.Fd(), unix.F_GETPATH, uintptr(unsafe.Pointer(&buffer[0])))
	if errno != 0 {
		return "", errno
	}
	path := string(buffer[:])
	if end := strings.IndexByte(path, 0); end >= 0 {
		path = path[:end]
	}
	if !strings.HasPrefix(path, "/dev/ttys") || len(path) == len("/dev/ttys") || filepath.Clean(path) != path {
		return "", errors.New("stdin is not an allocated pane slave PTY")
	}
	for _, ch := range strings.TrimPrefix(path, "/dev/ttys") {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z') {
			return "", errors.New("invalid pane slave PTY name")
		}
	}
	actual, err := file.Stat()
	if err != nil {
		return "", err
	}
	named, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if actual.Mode()&os.ModeCharDevice == 0 || !os.SameFile(actual, named) {
		return "", errors.New("pane slave PTY identity does not match stdin")
	}
	return path, nil
}
