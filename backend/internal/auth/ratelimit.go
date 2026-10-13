package auth

import (
	"sync"
	"time"
)

// failureLimiter counts failed attempts per key in a fixed window. It is
// in-memory and per-process, which is enough for a single replica.
type failureLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	hits   map[string]*window
}

type window struct {
	count int
	start time.Time
}

func newFailureLimiter(max int, w time.Duration) *failureLimiter {
	return &failureLimiter{max: max, window: w, now: time.Now, hits: map[string]*window{}}
}

// blocked returns how long key must wait, or 0 if it may try again.
func (l *failureLimiter) blocked(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.hits[key]
	if !ok {
		return 0
	}
	elapsed := l.now().Sub(w.start)
	if elapsed >= l.window {
		delete(l.hits, key)
		return 0
	}
	if w.count >= l.max {
		return l.window - elapsed
	}
	return 0
}

func (l *failureLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.hits) > 10000 {
		l.prune(now)
	}
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) >= l.window {
		l.hits[key] = &window{count: 1, start: now}
		return
	}
	w.count++
}

func (l *failureLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

func (l *failureLimiter) prune(now time.Time) {
	for k, w := range l.hits {
		if now.Sub(w.start) >= l.window {
			delete(l.hits, k)
		}
	}
}
