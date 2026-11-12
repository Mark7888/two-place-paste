package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
}

func TestLimiterPerIP(t *testing.T) {
	t.Parallel()

	clk := newTestClock()
	l := newLimiter(time.Second, 2, time.Millisecond, 1000, clk.now)

	for i := range 2 {
		if ok, _ := l.allow("198.51.100.7"); !ok {
			t.Fatalf("attempt %d from the first ip was refused, want allowed", i+1)
		}
	}
	ok, scope := l.allow("198.51.100.7")
	if ok || scope != "ip" {
		t.Errorf("third attempt = (%v, %q), want (false, \"ip\")", ok, scope)
	}

	// A different client has its own budget: one misbehaving IP must not lock
	// everyone else out.
	if ok, _ := l.allow("203.0.113.9"); !ok {
		t.Error("a second ip was refused, want allowed")
	}

	// The bucket refills over time.
	clk.advance(time.Second)
	if ok, _ := l.allow("198.51.100.7"); !ok {
		t.Error("after one refill interval the first ip was refused, want allowed")
	}
}

// TestLimiterGlobalCap is the half that matters against a distributed attempt:
// per-IP limits alone multiply by the number of source addresses.
func TestLimiterGlobalCap(t *testing.T) {
	t.Parallel()

	clk := newTestClock()
	l := newLimiter(time.Second, 100, time.Minute, 3, clk.now)

	for i := range 3 {
		if ok, _ := l.allow(fmt.Sprintf("198.51.100.%d", i)); !ok {
			t.Fatalf("attempt %d from a fresh ip was refused, want allowed", i+1)
		}
	}
	ok, scope := l.allow("198.51.100.250")
	if ok || scope != "global" {
		t.Errorf("attempt past the global cap = (%v, %q), want (false, \"global\")", ok, scope)
	}
}

// TestLimiterGlobalRefusalDoesNotChargeTheClient: a client refused by the
// global bucket keeps its own budget, so a busy server does not silently lock
// out an operator who has made no attempts of their own.
func TestLimiterGlobalRefusalDoesNotChargeTheClient(t *testing.T) {
	t.Parallel()

	clk := newTestClock()
	l := newLimiter(time.Hour, 2, time.Hour, 1, clk.now)

	if ok, _ := l.allow("198.51.100.1"); !ok {
		t.Fatal("first attempt refused, want allowed")
	}
	if ok, scope := l.allow("203.0.113.1"); ok || scope != "global" {
		t.Fatalf("second attempt = (%v, %q), want (false, \"global\")", ok, scope)
	}

	l.mu.Lock()
	got := l.ips["203.0.113.1"].tokens
	l.mu.Unlock()
	if got != 2 {
		t.Errorf("per-ip tokens after a global refusal = %v, want the full burst of 2", got)
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{
			name:       "direct connection",
			remoteAddr: "198.51.100.7:41234",
			want:       "198.51.100.7",
		},
		{
			name:       "forwarded header from a public peer is ignored",
			remoteAddr: "198.51.100.7:41234",
			forwarded:  "203.0.113.9",
			want:       "198.51.100.7",
		},
		{
			name:       "reverse proxy on loopback",
			remoteAddr: "127.0.0.1:5000",
			forwarded:  "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "only the rightmost entry is trusted",
			remoteAddr: "10.0.0.2:5000",
			forwarded:  "1.2.3.4, 5.6.7.8, 203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "garbage forwarded value falls back to the peer",
			remoteAddr: "10.0.0.2:5000",
			forwarded:  "not-an-ip",
			want:       "10.0.0.2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tt.forwarded)
			}
			if got := clientIP(r); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionStoreExpiry(t *testing.T) {
	t.Parallel()

	clk := newTestClock()
	s := newSessionStore(time.Hour, clk.now)

	sess := s.create()
	if got, ok := s.get(sess.id); !ok || got.csrf != sess.csrf {
		t.Fatalf("get() right after create = (%v, %v), want the session back", got, ok)
	}
	if _, ok := s.get("not-a-session"); ok {
		t.Error("get() with an unknown id returned a session")
	}

	clk.advance(time.Hour + time.Second)
	if _, ok := s.get(sess.id); ok {
		t.Error("an expired session is still readable, want it gone")
	}

	// Logging out drops the session before its TTL.
	live := s.create()
	s.destroy(live.id)
	if _, ok := s.get(live.id); ok {
		t.Error("a destroyed session is still readable, want it gone")
	}
}

func TestLimiterBlocked(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	l := newLimiter(time.Minute, 2, time.Second, 100, clk.now)

	if blocked, _ := l.blocked("192.0.2.1"); blocked {
		t.Fatal("an unseen client is blocked")
	}
	for range 2 {
		l.allow("192.0.2.1")
	}
	if blocked, scope := l.blocked("192.0.2.1"); !blocked || scope != "ip" {
		t.Errorf("blocked() = %v, %q; want true, %q", blocked, scope, "ip")
	}
	// Asking does not spend: after the refill one attempt is there.
	clk.advance(time.Minute)
	for range 3 {
		if blocked, _ := l.blocked("192.0.2.1"); blocked {
			t.Fatal("blocked() after a refill = true; asking must not spend attempts")
		}
	}
}

func TestKeyedLimiter(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	k := newKeyedLimiter(2, 3, clk.now)

	tests := []struct {
		advance       time.Duration
		key           string
		wantOK        bool
		wantRemaining int
	}{
		{0, "a", true, 2},
		{0, "a", true, 1},
		{0, "a", true, 0},
		{0, "a", false, 0},
		{0, "b", true, 2},
		{500 * time.Millisecond, "a", true, 0},
		{0, "a", false, 0},
	}
	for i, tt := range tests {
		clk.advance(tt.advance)
		res := k.take(tt.key)
		if res.ok != tt.wantOK || res.remaining != tt.wantRemaining || res.limit != 3 {
			t.Errorf("take %d (%s) = ok %v, remaining %d, limit %d; want %v, %d, 3",
				i+1, tt.key, res.ok, res.remaining, res.limit, tt.wantOK, tt.wantRemaining)
		}
		if !res.ok && res.retry <= 0 {
			t.Errorf("take %d refused with retry %v, want a positive wait", i+1, res.retry)
		}
	}
	// b refilled to full meanwhile, so this spends one: full again in half
	// a second at two a second.
	if res := k.take("b"); res.reset != 500*time.Millisecond {
		t.Errorf("reset = %v, want 500ms for one spent at two a second", res.reset)
	}
}
