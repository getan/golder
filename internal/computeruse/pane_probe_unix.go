//go:build unix

package computeruse

import (
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Probe the terminal itself rather than guessing from TERM. Silence or an
// error means no graphics; the pane remains usable through semantic controls.
func queryPaneGraphics(in, out *os.File) bool {
	if strings.EqualFold(os.Getenv("TERM_GRAPHICS"), "none") || in.Fd() >= 1024 {
		return false
	}
	if err := writePaneBytes(out, []byte("\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\")); err != nil {
		return false
	}
	fd := int(in.Fd())
	deadline := time.Now().Add(350 * time.Millisecond)
	var reply []byte
	for time.Now().Before(deadline) && len(reply) < 4096 {
		var set unix.FdSet
		set.Set(fd)
		tv := unix.NsecToTimeval(time.Until(deadline).Nanoseconds())
		n, err := unix.Select(fd+1, &set, nil, nil, &tv)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			break
		}
		buf := make([]byte, 256)
		n, err = unix.Read(fd, buf)
		if n > 0 {
			reply = append(reply, buf[:n]...)
			if paneGraphicsReply(string(reply)) {
				return true
			}
		}
		if err != nil && err != unix.EINTR && err != unix.EAGAIN {
			break
		}
	}
	return false
}
