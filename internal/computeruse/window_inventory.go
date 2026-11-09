package computeruse

import (
	"context"
	"errors"
)

// collectWindowInventory grows to fit a complete snapshot rather than silently
// selecting from a truncated list. The upper bound prevents unbounded memory.
func collectWindowInventory[T any](ctx context.Context, read func([]T) int) ([]T, error) {
	const initial, maximum = 128, 8192
	for size := initial; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		windows := make([]T, size)
		n := read(windows)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, errors.New("macOS window inventory unavailable")
		}
		if n <= size {
			return windows[:n], nil
		}
		if n > maximum {
			return nil, errors.New("macOS visible window inventory exceeded its safe limit; close windows and retry")
		}
		size = min(maximum, max(n, size*2))
	}
}
