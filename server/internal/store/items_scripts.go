package store

import "github.com/redis/go-redis/v9"

// Lua scripts of the store's records (SPEC §4.6). Every one that checks a
// limit, an epoch or the store switch writes in the same step, so two saves
// racing for the last bytes cannot both win, and a save cannot slip past a
// rekey that is refusing to run without a store link.

// reserveItemScript claims room for an item and writes its pending record.
//
// Return codes: 0 reserved, 1 no such group, 2 store off, 3 store being
// emptied, 4 epoch conflict, 5 item id in use, 6 does not fit.
//
//	KEYS[1] group key            ARGV[1] group id
//	KEYS[2] item key             ARGV[2] item id
//	KEYS[3] group store usage    ARGV[3] epoch
//	KEYS[4] total store usage    ARGV[4] blob size
//	KEYS[5] pending items        ARGV[5] accounted bytes
//	KEYS[6] groups being emptied ARGV[6] started, unix ms
//	                             ARGV[7] group limit, bytes (0: none)
//	                             ARGV[8] total limit, bytes (0: none)
//	                             ARGV[9] item limit
var reserveItemScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 1 end
if redis.call('HGET', KEYS[1], 'store_on') ~= '1' then return 2 end
if redis.call('SISMEMBER', KEYS[6], ARGV[1]) == 1 then return 3 end
if redis.call('HGET', KEYS[1], 'epoch') ~= ARGV[3] then return 4 end
if redis.call('EXISTS', KEYS[2]) == 1 then return 5 end

local acc = tonumber(ARGV[5])
local group_bytes = tonumber(redis.call('HGET', KEYS[3], 'bytes') or '0')
local group_items = tonumber(redis.call('HGET', KEYS[3], 'items') or '0')
local total_bytes = tonumber(redis.call('HGET', KEYS[4], 'bytes') or '0')
local group_limit = tonumber(ARGV[7])
local total_limit = tonumber(ARGV[8])
if group_limit > 0 and group_bytes + acc > group_limit then return 6 end
if total_limit > 0 and total_bytes + acc > total_limit then return 6 end
if group_items + 1 > tonumber(ARGV[9]) then return 6 end

redis.call('HSET', KEYS[2],
  'group_id', ARGV[1],
  'size', ARGV[4],
  'accounted', ARGV[5],
  'wrap_epoch', ARGV[3],
  'content_id', ARGV[2],
  'content_epoch', ARGV[3],
  'meta_rev', '0',
  'state', 'pending',
  'started_at', ARGV[6])
redis.call('HINCRBY', KEYS[3], 'bytes', acc)
redis.call('HINCRBY', KEYS[3], 'items', 1)
redis.call('HINCRBY', KEYS[4], 'bytes', acc)
redis.call('HINCRBY', KEYS[4], 'items', 1)
redis.call('ZADD', KEYS[5], ARGV[6], ARGV[2])
return 0
`)

// commitItemScript completes a pending item. It is the half of the rekey
// check that runs on the item's side: the epoch it compares is the one
// applyRekeyScript bumps, so an item at the old epoch is either listed before
// the rekey runs — and the rekey then needs a link — or refused here.
//
// Return codes: 0 committed, 1 no such pending item in the group, 2 no such
// group, 3 store off, 4 store being emptied, 5 epoch conflict.
//
//	KEYS[1] group key            ARGV[1] group id
//	KEYS[2] item key             ARGV[2] item id
//	KEYS[3] group store index    ARGV[3] epoch
//	KEYS[4] pending items        ARGV[4] wrapped item keys
//	KEYS[5] groups being emptied ARGV[5] sealed metadata
//	                             ARGV[6] saved, unix ms
var commitItemScript = redis.NewScript(`
if redis.call('HGET', KEYS[2], 'group_id') ~= ARGV[1] then return 1 end
if redis.call('HGET', KEYS[2], 'state') ~= 'pending' then return 1 end
if redis.call('EXISTS', KEYS[1]) == 0 then return 2 end
if redis.call('HGET', KEYS[1], 'store_on') ~= '1' then return 3 end
if redis.call('SISMEMBER', KEYS[5], ARGV[1]) == 1 then return 4 end
if redis.call('HGET', KEYS[1], 'epoch') ~= ARGV[3] then return 5 end
if redis.call('HGET', KEYS[2], 'wrap_epoch') ~= ARGV[3] then return 5 end

redis.call('HSET', KEYS[2],
  'keys', ARGV[4],
  'meta', ARGV[5],
  'saved_at', ARGV[6],
  'state', 'committed')
redis.call('ZADD', KEYS[3], ARGV[6], ARGV[2])
redis.call('ZREM', KEYS[4], ARGV[2])
return 0
`)

// releaseItemScript forgets a pending item and gives its room back.
//
// Return codes: 1 released, 0 not a pending item of the group.
//
//	KEYS[1] item key             ARGV[1] group id
//	KEYS[2] group store usage    ARGV[2] item id
//	KEYS[3] total store usage
//	KEYS[4] pending items
var releaseItemScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'group_id') ~= ARGV[1] then return 0 end
if redis.call('HGET', KEYS[1], 'state') ~= 'pending' then return 0 end
local acc = tonumber(redis.call('HGET', KEYS[1], 'accounted') or '0')
redis.call('HINCRBY', KEYS[2], 'bytes', -acc)
redis.call('HINCRBY', KEYS[2], 'items', -1)
redis.call('HINCRBY', KEYS[3], 'bytes', -acc)
redis.call('HINCRBY', KEYS[3], 'items', -1)
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[4], ARGV[2])
return 1
`)

// deleteItemScript deletes a committed item's record and gives its room
// back.
//
// Return codes: 1 deleted, 0 not a committed item of the group.
//
//	KEYS[1] item key             ARGV[1] group id
//	KEYS[2] group store index    ARGV[2] item id
//	KEYS[3] group store usage
//	KEYS[4] total store usage
var deleteItemScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'group_id') ~= ARGV[1] then return 0 end
if redis.call('HGET', KEYS[1], 'state') ~= 'committed' then return 0 end
local acc = tonumber(redis.call('HGET', KEYS[1], 'accounted') or '0')
redis.call('HINCRBY', KEYS[3], 'bytes', -acc)
redis.call('HINCRBY', KEYS[3], 'items', -1)
redis.call('HINCRBY', KEYS[4], 'bytes', -acc)
redis.call('HINCRBY', KEYS[4], 'items', -1)
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[2])
return 1
`)

// updateItemMetaScript replaces a committed item's sealed metadata when its
// revision is still the one the editor read.
//
// Return: the new revision; -1 not a committed item of the group; -2 the
// item has moved past the expected revision.
//
//	KEYS[1] item key             ARGV[1] group id
//	                             ARGV[2] expected revision
//	                             ARGV[3] sealed metadata
var updateItemMetaScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'group_id') ~= ARGV[1] then return -1 end
if redis.call('HGET', KEYS[1], 'state') ~= 'committed' then return -1 end
if redis.call('HGET', KEYS[1], 'meta_rev') ~= ARGV[2] then return -2 end
local rev = tonumber(ARGV[2]) + 1
redis.call('HSET', KEYS[1], 'meta', ARGV[3], 'meta_rev', tostring(rev))
return rev
`)
