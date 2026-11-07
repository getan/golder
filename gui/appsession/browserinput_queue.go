package appsession

import "sync"

// browserInputQueue coalesces only adjacent pointer moves. One slot is
// reserved for release so overload cannot leave a successfully queued drag
// pressed. Other boundary events are rejected explicitly, never silently lost.
type browserInputQueue struct {
	mu    sync.Mutex
	items []surfaceInput
	wake  chan struct{}
	limit int
}

func newBrowserInputQueue(limit int) *browserInputQueue {
	return &browserInputQueue{limit: limit, wake: make(chan struct{}, 1)}
}

func (q *browserInputQueue) push(input surfaceInput) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if input.event.Kind == BrowserPointerMove && len(q.items) > 0 {
		last := len(q.items) - 1
		if q.items[last].event.Kind == BrowserPointerMove && q.items[last].target == input.target {
			q.items[last] = input
			return true
		}
	}
	limit := q.limit
	if input.event.Kind != BrowserPointerUp {
		limit--
	}
	if len(q.items) >= limit {
		return false
	}
	q.items = append(q.items, input)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *browserInputQueue) pop() (surfaceInput, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return surfaceInput{}, false
	}
	input := q.items[0]
	q.items[0] = surfaceInput{}
	q.items = q.items[1:]
	if len(q.items) > 0 {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
	return input, true
}
