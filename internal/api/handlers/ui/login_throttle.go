package ui

import (
	"sync"
	"time"
)

const (
	// loginFailureLimit failed sign-ins within loginFailureWindow lock a
	// client out until the window has passed since its first failure.
	loginFailureLimit  = 10
	loginFailureWindow = 15 * time.Minute

	// loginTrackedClients bounds the table; past it, expired records are
	// pruned and, if that is not enough, the table is reset. Resetting only
	// ever errs towards letting a sign-in attempt through.
	loginTrackedClients = 10_000
)

// loginThrottle counts failed panel sign-ins per client. The global rate
// limiter allows a thousand requests a minute, which is far too many guesses
// at an API key.
type loginThrottle struct {
	mu      sync.Mutex
	clients map[string]*loginRecord
	now     func() time.Time
}

type loginRecord struct {
	failures int
	since    time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{clients: make(map[string]*loginRecord), now: time.Now}
}

// blocked returns how long the client still has to wait, or 0.
func (t *loginThrottle) blocked(client string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec, ok := t.clients[client]
	if !ok {
		return 0
	}
	elapsed := t.now().Sub(rec.since)
	if elapsed > loginFailureWindow {
		delete(t.clients, client)
		return 0
	}
	if rec.failures < loginFailureLimit {
		return 0
	}
	return loginFailureWindow - elapsed
}

// fail records a failed attempt.
func (t *loginThrottle) fail(client string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	rec, ok := t.clients[client]
	if !ok || now.Sub(rec.since) > loginFailureWindow {
		if len(t.clients) >= loginTrackedClients {
			t.pruneLocked(now)
		}
		rec = &loginRecord{since: now}
		t.clients[client] = rec
	}
	rec.failures++
}

// succeed clears the client's record.
func (t *loginThrottle) succeed(client string) {
	t.mu.Lock()
	delete(t.clients, client)
	t.mu.Unlock()
}

func (t *loginThrottle) pruneLocked(now time.Time) {
	for client, rec := range t.clients {
		if now.Sub(rec.since) > loginFailureWindow {
			delete(t.clients, client)
		}
	}
	if len(t.clients) >= loginTrackedClients {
		clear(t.clients)
	}
}
