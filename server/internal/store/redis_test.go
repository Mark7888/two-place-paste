package store_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// redisAddr is the integration-test Redis. CI runs it as a service container;
// a developer runs `redis-server` locally. When neither is there the Redis
// half of every test skips with a reason rather than failing
// (docs/conventions.md §5).
func redisAddr() string {
	if addr := os.Getenv("TPP_TEST_REDIS_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:6379"
}

func newTestRedisStore(t *testing.T) store.Store {
	t.Helper()

	addr := redisAddr()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })

	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis not available at %s: %v", addr, err)
	}
	return store.NewRedisStore(rdb)
}

// TestConsumeTokenConcurrent is the race the Lua CAS exists for: one token
// must produce exactly one group no matter how many clients present it at the
// same instant (SPEC §3.1 step 5).
func TestConsumeTokenConcurrent(t *testing.T) {
	t.Parallel()

	s := newTestRedisStore(t)
	ctx := context.Background()

	tok, err := s.CreateToken(ctx, "contended")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}

	const callers = 50
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		okCount int
		errs    []error
	)
	start := make(chan struct{})
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.ConsumeToken(ctx, tok.Value)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				okCount++
			} else {
				errs = append(errs, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if okCount != 1 {
		t.Errorf("successful ConsumeToken() calls = %d, want 1", okCount)
	}
	for _, err := range errs {
		if !errors.Is(err, store.ErrTokenConsumed) {
			t.Errorf("losing ConsumeToken() error = %v, want %v", err, store.ErrTokenConsumed)
		}
	}
}
