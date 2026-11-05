//go:build !windows

package computeruse

import "context"

func verifyPlatformOperatorPanePeer(ctx context.Context, daemon panePeerAuthority, windowID string, pid int, start uint64) error {
	return verifyOperatorPanePeer(ctx, daemon, windowID, pid, start)
}
