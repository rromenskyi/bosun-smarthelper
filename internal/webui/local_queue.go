package webui

import "sync"

// localQueue serializes chat requests that the (weak, shared-CPU) local
// model would serve — unlike the remote provider, it can't usefully run
// more than one generation at a time. Remote-served requests never touch
// this at all (see handleChat): only local ones queue, and they queue with
// a visible position instead of blocking silently — see docs/streaming.md.
type localQueue struct {
	mu      sync.Mutex
	holding bool
	waiters []chan struct{}
}

// join returns a channel that receives exactly once, when it's this
// caller's turn, and position — 0 if the slot was free (caller may proceed
// immediately), otherwise how many turns are ahead of this one (1 = next
// up, right after whoever's currently being served).
func (q *localQueue) join() (turn chan struct{}, position int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ch := make(chan struct{}, 1)
	if !q.holding {
		q.holding = true
		ch <- struct{}{}
		return ch, 0
	}
	q.waiters = append(q.waiters, ch)
	return ch, len(q.waiters)
}

// tryHold claims the slot only if it's free right now, without queueing —
// used by background maintenance (see Server.TryIdleAfter), which should
// skip its turn entirely rather than wait if the local model is busy.
func (q *localQueue) tryHold() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.holding {
		return false
	}
	q.holding = true
	return true
}

// release lets the next waiter (if any) take its turn; otherwise marks the
// slot free. Call exactly once after a granted turn (position 0, or after
// receiving from turn) is done being used.
func (q *localQueue) release() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.waiters) == 0 {
		q.holding = false
		return
	}
	next := q.waiters[0]
	q.waiters = q.waiters[1:]
	next <- struct{}{}
}

// abandon gives up a not-yet-granted turn (e.g. the caller's own context
// timed out while waiting) — removes it from the queue. Safe even if turn
// was granted concurrently right as this is called: that turn is then
// immediately passed on to the next waiter instead of being lost, so
// abandoning never leaves the queue stuck thinking someone is still
// holding a slot they never used.
func (q *localQueue) abandon(turn chan struct{}) {
	q.mu.Lock()
	select {
	case <-turn:
		// Our turn had already arrived — pass it on.
		q.mu.Unlock()
		q.release()
		return
	default:
	}
	for i, w := range q.waiters {
		if w == turn {
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			break
		}
	}
	q.mu.Unlock()
}
