package quota

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis key layout. Nothing here carries a TTL: these keys are bookkeeping for
// the whole deployment, and `volatile-lru` must never be able to evict them
// (SPEC §4.2). Expiry is handled by score, through Expire.
//
//	quota:index              zset    entry id scored by expires_at (unix ms)
//	quota:usage              string  accounted bytes, all groups
//	quota:entry:<id>         hash    group, size, pending, expires_at
//	quota:group:<id>:index   zset    the group's entry ids, scored as above
//	quota:group:<id>:usage   string  the group's accounted bytes
const (
	// defaultNamespace prefixes every key. Tests use their own, so that
	// parallel tests sharing one Redis cannot see each other's totals.
	defaultNamespace = "quota:"

	// evictionStatsTTL keeps an hourly eviction counter a little past the
	// day the dashboard reports on.
	evictionStatsTTL = 25 * 60 * 60

	// expireBatch bounds the work one script call does when forgetting
	// expired entries, so a sweep after long downtime cannot hold Redis for
	// long in a single call.
	expireBatch = 1000
)

// keys is the key layout under one namespace.
type keys struct{ ns string }

func (k keys) index() string                    { return k.ns + "index" }
func (k keys) usage() string                    { return k.ns + "usage" }
func (k keys) entryPrefix() string              { return k.ns + "entry:" }
func (k keys) groupPrefix() string              { return k.ns + "group:" }
func (k keys) entry(id string) string           { return k.entryPrefix() + id }
func (k keys) groupIndex(groupID string) string { return k.groupPrefix() + groupID + ":index" }
func (k keys) groupUsage(groupID string) string { return k.groupPrefix() + groupID + ":usage" }

// evictions is the hourly eviction counter for the UTC hour holding t. It
// carries a TTL: it is a statistic for the dashboard, not bookkeeping, and
// losing one costs a number on a screen.
func (k keys) evictions(t time.Time) string {
	return k.ns + "evictions:" + t.UTC().Format("2006010215")
}

func (k keys) pattern() string { return k.ns + "*" }

// luaHelpers is shared by every script. Like internal/store's scripts it builds
// per-group key names from a prefix rather than receiving each in KEYS; that is
// safe only because the deployment is a single Redis instance, never a cluster.
//
// Sizes are passed to INCRBY/DECRBY as the strings they were stored as. A Lua
// number would be formatted in exponent notation above 1e14 and refused.
const luaHelpers = `
local P_ENTRY, P_GROUP = ARGV[1], ARGV[2]

local function forget(id)
  local f = redis.call('HMGET', P_ENTRY .. id, 'group', 'size')
  redis.call('ZREM', KEYS[1], id)
  if not f[1] then return end
  redis.call('ZREM', P_GROUP .. f[1] .. ':index', id)
  redis.call('DECRBY', KEYS[2], f[2])
  redis.call('DECRBY', P_GROUP .. f[1] .. ':usage', f[2])
  redis.call('DEL', P_ENTRY .. id)
end

local function expire(now, limit)
  local ids = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now, 'LIMIT', 0, limit)
  for _, id in ipairs(ids) do forget(id) end
  return #ids
end
`

// reserveScript is the whole of Reserve: forget what has expired, decide the
// victims, and only then — once the entry is known to fit — apply the
// evictions and account the new entry. A refusal changes nothing beyond the
// expiry pass, which was due anyway.
//
// Reply: {0, victim id, victim group, victim size, ...} on success; {1} the
// entry alone exceeds a limit; {2} the id is already accounted; {3} eviction
// cannot free enough room.
//
//	KEYS[1] quota:index      ARGV[1] entry prefix    ARGV[6] expires_at (ms)
//	KEYS[2] quota:usage      ARGV[2] group prefix    ARGV[7] now (ms)
//	KEYS[3] eviction hour    ARGV[3] entry id        ARGV[8] total limit (0 = none)
//	                         ARGV[4] group id        ARGV[9] group limit (0 = none)
//	                         ARGV[5] size
var reserveScript = redis.NewScript(luaHelpers + `
expire(ARGV[7], ` + strconv.Itoa(expireBatch) + `)

local id, g, size = ARGV[3], ARGV[4], tonumber(ARGV[5])
local totalLimit, groupLimit = tonumber(ARGV[8]), tonumber(ARGV[9])

if redis.call('EXISTS', P_ENTRY .. id) == 1 then return {2} end
if (groupLimit > 0 and size > groupLimit) or (totalLimit > 0 and size > totalLimit) then
  return {1}
end

local total = tonumber(redis.call('GET', KEYS[2]) or '0')
local groupUsed = tonumber(redis.call('GET', P_GROUP .. g .. ':usage') or '0')
local chosen, victims = {}, {}

-- scan walks an index oldest first, choosing committed entries until fits()
-- holds or the index runs out. Pending entries are uploads in flight; they
-- are never victims.
local function scan(index, fits)
  local start = 0
  while not fits() do
    local ids = redis.call('ZRANGE', index, start, start + 99)
    if #ids == 0 then return end
    for _, v in ipairs(ids) do
      if fits() then return end
      if not chosen[v] then
        local f = redis.call('HMGET', P_ENTRY .. v, 'group', 'size', 'pending')
        if f[1] and f[3] ~= '1' then
          local vs = tonumber(f[2])
          chosen[v] = true
          total = total - vs
          if f[1] == g then groupUsed = groupUsed - vs end
          victims[#victims + 1] = {v, f[1], f[2]}
        end
      end
    end
    start = start + 100
  end
end

if groupLimit > 0 then
  scan(P_GROUP .. g .. ':index', function() return groupUsed + size <= groupLimit end)
  if groupUsed + size > groupLimit then return {3} end
end
if totalLimit > 0 then
  scan(KEYS[1], function() return total + size <= totalLimit end)
  if total + size > totalLimit then return {3} end
end

local out = {0}
for _, v in ipairs(victims) do
  forget(v[1])
  out[#out + 1] = v[1]
  out[#out + 1] = v[2]
  out[#out + 1] = v[3]
  redis.call('HINCRBY', KEYS[3], 'count', 1)
  redis.call('HINCRBY', KEYS[3], 'bytes', v[3])
end
if #victims > 0 then
  redis.call('EXPIRE', KEYS[3], ` + strconv.Itoa(evictionStatsTTL) + `)
end

redis.call('HSET', P_ENTRY .. id, 'group', g, 'size', ARGV[5], 'pending', '1', 'expires_at', ARGV[6])
redis.call('ZADD', KEYS[1], ARGV[6], id)
redis.call('ZADD', P_GROUP .. g .. ':index', ARGV[6], id)
redis.call('INCRBY', KEYS[2], ARGV[5])
redis.call('INCRBY', P_GROUP .. g .. ':usage', ARGV[5])
return out
`)

// releaseScript forgets one entry.
//
//	KEYS[1] quota:index   ARGV[1] entry prefix   ARGV[3] entry id
//	KEYS[2] quota:usage   ARGV[2] group prefix
var releaseScript = redis.NewScript(luaHelpers + `
forget(ARGV[3])
return 0
`)

// expireScript forgets up to ARGV[4] entries expired at ARGV[3] and returns
// how many it forgot.
//
//	KEYS[1] quota:index   ARGV[1] entry prefix   ARGV[3] now (ms)
//	KEYS[2] quota:usage   ARGV[2] group prefix   ARGV[4] batch size
var expireScript = redis.NewScript(luaHelpers + `
return expire(ARGV[3], tonumber(ARGV[4]))
`)

// commitScript clears the pending flag of an entry that is still accounted.
//
//	KEYS[1] entry key
var commitScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  redis.call('HSET', KEYS[1], 'pending', '0')
end
return 0
`)

// Redis is the Ledger that ships.
type Redis struct {
	rdb    redis.Cmdable
	limits Limits
	now    func() time.Time
	k      keys
}

// NewRedis returns a ledger over rdb.
func NewRedis(rdb redis.Cmdable, opts Options) *Redis {
	return newRedis(rdb, opts, defaultNamespace)
}

func newRedis(rdb redis.Cmdable, opts Options, namespace string) *Redis {
	return &Redis{rdb: rdb, limits: opts.Limits, now: opts.now(), k: keys{ns: namespace}}
}

var _ Ledger = (*Redis)(nil)

func ms(t time.Time) string { return strconv.FormatInt(t.UTC().UnixMilli(), 10) }

// Reserve implements Ledger.
func (q *Redis) Reserve(ctx context.Context, r Reservation) ([]Victim, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	now := q.now()
	reply, err := reserveScript.Run(ctx, q.rdb, []string{q.k.index(), q.k.usage(), q.k.evictions(now)},
		q.k.entryPrefix(), q.k.groupPrefix(),
		r.EntryID, r.GroupID, strconv.FormatInt(r.Size, 10),
		ms(r.ExpiresAt), ms(now),
		strconv.FormatInt(q.limits.TotalBytes, 10), strconv.FormatInt(q.limits.GroupBytes, 10),
	).Slice()
	if err != nil {
		return nil, fmt.Errorf("reserve %s: %w", r.EntryID, err)
	}
	if len(reply) == 0 {
		return nil, fmt.Errorf("reserve %s: empty script reply", r.EntryID)
	}
	code, _ := reply[0].(int64)
	switch code {
	case 0:
	case 1:
		return nil, fmt.Errorf("reserve %s: %w", r.EntryID, ErrTooLarge)
	case 2:
		return nil, fmt.Errorf("reserve %s: %w", r.EntryID, ErrExists)
	case 3:
		return nil, fmt.Errorf("reserve %s: %w", r.EntryID, ErrExceeded)
	default:
		return nil, fmt.Errorf("reserve %s: unexpected script result %v", r.EntryID, reply[0])
	}

	rest := reply[1:]
	if len(rest)%3 != 0 {
		return nil, fmt.Errorf("reserve %s: malformed victim list of %d fields", r.EntryID, len(rest))
	}
	victims := make([]Victim, 0, len(rest)/3)
	for i := 0; i < len(rest); i += 3 {
		id, _ := rest[i].(string)
		group, _ := rest[i+1].(string)
		sizeStr, _ := rest[i+2].(string)
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("reserve %s: victim %s size %q: %w", r.EntryID, id, sizeStr, err)
		}
		victims = append(victims, Victim{EntryID: id, GroupID: group, Size: size})
	}
	return victims, nil
}

// Commit implements Ledger.
func (q *Redis) Commit(ctx context.Context, entryID string) error {
	if err := commitScript.Run(ctx, q.rdb, []string{q.k.entry(entryID)}).Err(); err != nil {
		return fmt.Errorf("commit %s: %w", entryID, err)
	}
	return nil
}

// Release implements Ledger.
func (q *Redis) Release(ctx context.Context, entryID string) error {
	err := releaseScript.Run(ctx, q.rdb, []string{q.k.index(), q.k.usage()}, q.k.entryPrefix(), q.k.groupPrefix(), entryID).Err()
	if err != nil {
		return fmt.Errorf("release %s: %w", entryID, err)
	}
	return nil
}

// Expire implements Ledger. It works in batches so that no single script
// holds Redis for long, however much expired while the server was down.
func (q *Redis) Expire(ctx context.Context, now time.Time) (int, error) {
	total := 0
	for {
		n, err := expireScript.Run(ctx, q.rdb, []string{q.k.index(), q.k.usage()},
			q.k.entryPrefix(), q.k.groupPrefix(), ms(now), expireBatch).Int()
		if err != nil {
			return total, fmt.Errorf("expire accounted entries: %w", err)
		}
		total += n
		if n < expireBatch {
			return total, nil
		}
	}
}

// Usage implements Ledger.
func (q *Redis) Usage(ctx context.Context) (Usage, error) {
	return q.usage(ctx, q.k.usage(), q.k.index())
}

// GroupUsage implements Ledger.
func (q *Redis) GroupUsage(ctx context.Context, groupID string) (Usage, error) {
	return q.usage(ctx, q.k.groupUsage(groupID), q.k.groupIndex(groupID))
}

func (q *Redis) usage(ctx context.Context, bytesKey, index string) (Usage, error) {
	pipe := q.rdb.Pipeline()
	bytesCmd := pipe.Get(ctx, bytesKey)
	countCmd := pipe.ZCard(ctx, index)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return Usage{}, fmt.Errorf("read storage usage: %w", err)
	}
	var u Usage
	if v, err := bytesCmd.Int64(); err == nil {
		u.Bytes = v
	} else if !errors.Is(err, redis.Nil) {
		return Usage{}, fmt.Errorf("read storage usage: %w", err)
	}
	u.Entries = countCmd.Val()
	return u, nil
}

// Evictions implements Ledger: the sum of the hourly counters covering the
// last day, so the window is a day to within the hour.
func (q *Redis) Evictions(ctx context.Context) (Evictions, error) {
	now := q.now()
	pipe := q.rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, 0, evictionWindowHours)
	for h := range evictionWindowHours {
		cmds = append(cmds, pipe.HGetAll(ctx, q.k.evictions(now.Add(-time.Duration(h)*time.Hour))))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return Evictions{}, fmt.Errorf("read eviction counters: %w", err)
	}
	var ev Evictions
	for _, cmd := range cmds {
		fields := cmd.Val()
		n, _ := strconv.ParseInt(fields["count"], 10, 64)
		b, _ := strconv.ParseInt(fields["bytes"], 10, 64)
		ev.Count += n
		ev.Bytes += b
	}
	return ev, nil
}

// Rebuild implements Ledger. The deletion of the old ledger and the writing of
// the new one are one MULTI transaction, so a running server never sees a
// half-built ledger.
func (q *Redis) Rebuild(ctx context.Context, entries []Reservation) error {
	for _, r := range entries {
		if err := r.validate(); err != nil {
			return err
		}
	}

	var stale []string
	var cursor uint64
	for {
		keys, next, err := q.rdb.Scan(ctx, cursor, q.k.pattern(), 512).Result()
		if err != nil {
			return fmt.Errorf("rebuild accounting: scan: %w", err)
		}
		stale = append(stale, keys...)
		if next == 0 {
			break
		}
		cursor = next
	}

	var total int64
	groups := make(map[string]int64)
	pipe := q.rdb.TxPipeline()
	if len(stale) > 0 {
		pipe.Del(ctx, stale...)
	}
	for _, r := range entries {
		exp := ms(r.ExpiresAt)
		score, _ := strconv.ParseFloat(exp, 64)
		pipe.HSet(ctx, q.k.entry(r.EntryID), "group", r.GroupID, "size", r.Size, "pending", "0", "expires_at", exp)
		pipe.ZAdd(ctx, q.k.index(), redis.Z{Score: score, Member: r.EntryID})
		pipe.ZAdd(ctx, q.k.groupIndex(r.GroupID), redis.Z{Score: score, Member: r.EntryID})
		total += r.Size
		groups[r.GroupID] += r.Size
	}
	pipe.Set(ctx, q.k.usage(), total, 0)
	for g, n := range groups {
		pipe.Set(ctx, q.k.groupUsage(g), n, 0)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("rebuild accounting: write: %w", err)
	}
	return nil
}
