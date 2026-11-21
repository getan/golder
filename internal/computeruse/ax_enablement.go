package computeruse

import (
	"context"
	"time"
)

// One entry per catalog, keyed by process lifetime (not PID alone).
type axProcess struct {
	pid       int32
	sec, usec uint64
}

type axEnablement struct {
	process  axProcess
	hint     string
	electron bool
	retry    time.Time
}

func (a axEnablement) needsRequest(p axProcess, now time.Time) bool {
	return a.process != p || a.hint == "" || (!a.retry.IsZero() && !now.Before(a.retry))
}

func waitForAXRefresh(ctx context.Context, delay time.Duration) bool {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return ctx.Err() == nil
	}
}

func observedWebContent(electron bool, elements []Element, truncated bool) string {
	if !electron {
		return ""
	}
	for _, e := range elements {
		if e.Role == "AXWebArea" {
			return "web_area_observed"
		}
	}
	if truncated {
		return "not_observed_in_bounded_tree"
	}
	return "web_area_not_observed"
}
