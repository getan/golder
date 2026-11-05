package computeruse

import (
	"context"
	"errors"
	"net"

	"github.com/getan/golder/internal/tuiosctl"
)

type panePeerAuthority interface {
	CallInto(context.Context, string, any, any) error
	Session() string
	PaneID() string
}

// VerifyOperatorPanePeer binds a private pane connection to the exact helper
// pane Golder created. A claimed pane ID or a token in argv is not sufficient:
// the kernel supplies the peer PID and the daemon resolves that PID's pane.
// An older or restricted daemon that cannot prove this fails closed.
func VerifyOperatorPanePeer(ctx context.Context, conn net.Conn, daemon *tuiosctl.Client, windowID string) error {
	if daemon == nil || conn == nil || windowID == "" || windowID == daemon.PaneID() {
		return errors.New("computer pane identity unavailable")
	}
	pid, start, err := operatorPeerIdentity(conn)
	if err != nil {
		return err
	}
	return verifyPlatformOperatorPanePeer(ctx, daemon, windowID, pid, start)
}

func verifyOperatorPanePeer(ctx context.Context, daemon panePeerAuthority, windowID string, pid int, start uint64) error {
	if daemon == nil || windowID == "" || windowID == daemon.PaneID() || daemon.Session() == "" || pid <= 0 || start == 0 {
		return errors.New("computer pane identity unavailable")
	}
	var reply struct {
		Pane    bool   `json:"pane"`
		PID     int    `json:"peer_pid"`
		Window  string `json:"window"`
		Session string `json:"session"`
		Via     string `json:"via"`
	}
	params := map[string]any{"peer_pid": pid, "peer_start": start}
	if err := daemon.CallInto(ctx, "pane-grants", params, &reply); err != nil {
		return errors.New("computer pane peer could not be verified")
	}
	if !reply.Pane || reply.PID != pid || reply.Window != windowID || reply.Session != daemon.Session() || reply.Via != "pid" {
		return errors.New("computer pane peer identity mismatch")
	}
	return nil
}

// verifyListedOperatorPane is used only where the OS supplies the peer PID but
// the daemon cannot resolve PID ancestry. A directly launched helper must be
// the exact process reported for the newly created local pane.
func verifyListedOperatorPane(ctx context.Context, daemon panePeerAuthority, windowID string, pid int, start uint64) error {
	if daemon == nil || windowID == "" || windowID == daemon.PaneID() || daemon.Session() == "" || pid <= 0 || start == 0 {
		return errors.New("computer pane identity unavailable")
	}
	var reply struct {
		Windows []struct {
			ID   string `json:"window_id"`
			PID  int    `json:"pid"`
			Host string `json:"host"`
		} `json:"windows"`
	}
	if err := daemon.CallInto(ctx, "list-windows", map[string]any{"session": daemon.Session()}, &reply); err != nil {
		return errors.New("computer pane process could not be verified")
	}
	matches := 0
	for _, window := range reply.Windows {
		if window.ID == windowID {
			if window.PID != pid || window.Host != "" {
				return errors.New("computer pane process identity mismatch")
			}
			matches++
		} else if window.PID == pid {
			return errors.New("computer pane process identity is ambiguous")
		}
	}
	if matches != 1 {
		return errors.New("computer pane process identity unavailable")
	}
	return nil
}
