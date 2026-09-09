package store

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

func dialTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	addr := os.Getenv("TPP_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis not available at %s: %v", addr, err)
	}
	return rdb
}

// TestApplyRekeyPartialApply kills the rekey mid-way and asserts the group
// survives at its old epoch (SPEC §3.3 step 4, ROADMAP P3 acceptance).
//
// Redis does not roll back the writes a failing script already performed, so
// atomicity here is a property of the script's write order rather than of a
// transaction: every wrapped key is written first, and the epoch bump and the
// device deletion come last. An apply that dies in between therefore leaves a
// group that is still at the old epoch with the old device still present —
// the client retries and nothing is lost. This test injects that failure at
// exactly the point the ordering is designed for.
func TestApplyRekeyPartialApply(t *testing.T) {
	t.Parallel()

	rdb := dialTestRedis(t)
	ctx := context.Background()

	s := NewRedisStore(rdb)
	tok, err := s.CreateToken(ctx, "rekey interruption")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	created, err := s.CreateGroup(ctx, tok.Value, NewDevice{Name: "a", PublicKey: []byte("pk-a"), WrappedGroupKey: []byte("w-a")})
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	gid, a := created.Group.ID, created.Device.ID
	b, err := s.AddDevice(ctx, gid, NewDevice{Name: "b", PublicKey: []byte("pk-b"), WrappedGroupKey: []byte("w-b")})
	if err != nil {
		t.Fatalf("AddDevice() error = %v", err)
	}

	interrupted := NewRedisStore(rdb)
	body := strings.Replace(applyRekeyBody, "-- FAILPOINT", `return redis.error_reply('injected interruption')`, 1)
	if body == applyRekeyBody {
		t.Fatal("failpoint marker not found in applyRekeyBody; the test seam has moved")
	}
	interrupted.applyRekey = redis.NewScript(body)

	if _, err := interrupted.ApplyRekey(ctx, gid, b.ID, 1, []RekeyKey{{DeviceID: a, Key: []byte("w2-a")}}); err == nil {
		t.Fatal("interrupted ApplyRekey() error = nil, want the injected failure")
	}

	group, err := s.GetGroup(ctx, gid)
	if err != nil {
		t.Fatalf("GetGroup() error = %v", err)
	}
	if group.Epoch != 1 {
		t.Errorf("epoch after interrupted rekey = %d, want 1", group.Epoch)
	}
	if _, err := s.GetDevice(ctx, b.ID); err != nil {
		t.Errorf("GetDevice(revoked) error = %v, want the device to have survived the interrupted rekey", err)
	}

	// The retry must still work: the interruption left no state that blocks it.
	group, err = s.ApplyRekey(ctx, gid, b.ID, 1, []RekeyKey{{DeviceID: a, Key: []byte("w2-a")}})
	if err != nil {
		t.Fatalf("retried ApplyRekey() error = %v", err)
	}
	if group.Epoch != 2 {
		t.Errorf("epoch after retried rekey = %d, want 2", group.Epoch)
	}
}
