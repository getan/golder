package selfupdate

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/getan/golder/internal/downloadnet"
)

// CheckInterval is shared by the desktop GUI and the interactive TUI.
const CheckInterval = time.Hour

// Monitor schedules serialized metadata checks until the frontend closes.
// Startup checks and installation remain explicit frontend decisions.
func Monitor(ctx context.Context, check func()) {
	ticker := time.NewTicker(CheckInterval)
	defer ticker.Stop()
	monitorTicks(ctx, ticker.C, check)
}

func monitorTicks(ctx context.Context, ticks <-chan time.Time, check func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok || ctx.Err() != nil {
				return
			}
			check()
		}
	}
}

// CheckLatest refreshes release metadata regardless of the startup cache's TTL.
// This is a metadata-only request, not a download or installation. Successful
// checks also refresh the shared cache for later startups.
func CheckLatest(ctx context.Context, current string) (string, error) {
	return checkLatest(ctx, current, func(ctx context.Context, current string) (string, error) {
		client := &http.Client{Timeout: 10 * time.Second, Transport: downloadnet.Transport()}
		defer client.CloseIdleConnections()
		return LatestTag(ctx, client, Repo, current)
	})
}

func checkLatest(ctx context.Context, current string, fetch func(context.Context, string) (string, error)) (string, error) {
	if _, comparable := UpdateAvailable(current, current); !comparable {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	tag, err := fetch(ctx, current)
	if err != nil {
		return "", err
	}
	if !stableTag.MatchString(tag) {
		return "", errors.New("update server returned an invalid stable version")
	}
	writeCache(tag)
	return tag, nil
}
