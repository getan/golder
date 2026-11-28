//go:build !windows

package browserpool

import "time"

func browserHubReadyTimeout() time.Duration { return 10 * time.Second }
