package admin

import (
	"sync"
	"time"
)

// limiter rate limits the login endpoint: a token bucket per client IP plus a
// global bucket (SPEC §4.4, "rate limiting on the login endpoint").
//
// The global bucket is the half that matters against a real attacker: per-IP
// limits alone multiply by the number of source addresses, and this UI is
// publicly reachable. The per-IP bucket keeps one misconfigured client from
// spending the global allowance for everyone.
type limiter struct {
	perIPInterval  time.Duration
	perIPBurst     float64
	globalInterval time.Duration
	globalBurst    float64
	now            func() time.Time

	mu     sync.Mutex
	ips    map[string]*bucket
	global *bucket
}

// maxTrackedIPs bounds the per-IP map so that spoofed sources cannot grow it
// without limit. When the bound is hit, full buckets — clients that have not
// been attempting logins — are dropped first; they are indistinguishable from
// a client that was never seen.
const maxTrackedIPs = 4096

func newLimiter(perIPInterval time.Duration, perIPBurst int, globalInterval time.Duration, globalBurst int, now func() time.Time) *limiter {
	l := &limiter{
		perIPInterval:  perIPInterval,
		perIPBurst:     float64(perIPBurst),
		globalInterval: globalInterval,
		globalBurst:    float64(globalBurst),
		now:            now,
		ips:            make(map[string]*bucket),
	}
	l.global = &bucket{tokens: l.globalBurst, updated: now()}
	return l
}

// allow reports whether an attempt from ip may proceed and, when it may not,
// which bucket refused it. Both buckets are charged only when both permit the
// attempt, so a global refusal does not also consume the client's own budget.
func (l *limiter) allow(ip string) (ok bool, scope string) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, seen := l.ips[ip]
	if !seen {
		l.evictLocked()
		b = &bucket{tokens: l.perIPBurst, updated: now}
		l.ips[ip] = b
	}
	b.refill(now, l.perIPInterval, l.perIPBurst)
	l.global.refill(now, l.globalInterval, l.globalBurst)

	switch {
	case b.tokens < 1:
		return false, "ip"
	case l.global.tokens < 1:
		return false, "global"
	}
	b.tokens--
	l.global.tokens--
	return true, ""
}

// evictLocked drops full (idle) buckets once the map reaches its bound.
func (l *limiter) evictLocked() {
	if len(l.ips) < maxTrackedIPs {
		return
	}
	now := l.now()
	for ip, b := range l.ips {
		b.refill(now, l.perIPInterval, l.perIPBurst)
		if b.tokens >= l.perIPBurst {
			delete(l.ips, ip)
		}
	}
	// Every tracked client is mid-penalty: keep the existing entries and
	// refuse to grow. A new client is then limited to the global bucket,
	// which is the safe direction to fail in.
	if len(l.ips) >= maxTrackedIPs {
		l.ips = make(map[string]*bucket, maxTrackedIPs)
	}
}

// bucket is one token bucket. It holds fractional tokens so that a refill
// interval longer than the gap between attempts still accrues credit.
type bucket struct {
	tokens  float64
	updated time.Time
}

func (b *bucket) refill(now time.Time, interval time.Duration, burst float64) {
	if !now.After(b.updated) {
		return
	}
	b.tokens += float64(now.Sub(b.updated)) / float64(interval)
	if b.tokens > burst {
		b.tokens = burst
	}
	b.updated = now
}
