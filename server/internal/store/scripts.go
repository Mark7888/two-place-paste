package store

import "github.com/redis/go-redis/v9"

// Lua scripts for the two operations that must not be observable half-done.
//
// Both construct some key names inside the script from a prefix in ARGV rather
// than receiving every key in KEYS. That is safe here and only here: the
// deployment is a single Redis instance (SPEC §4.1), never a cluster, so key
// hashing across slots does not arise. If Redis Cluster ever becomes a target,
// these scripts must be revisited before anything else.

// createGroupScript turns a creation token into a group, and optionally into
// its first device, in one step (SPEC §3.1 steps 4-5).
//
// Return codes: 0 created, 1 no such token, 2 token already consumed.
//
//	KEYS[1] token key            ARGV[1] token / group id
//	KEYS[2] group key            ARGV[2] now, unix ms
//	KEYS[3] group devices set    ARGV[3] device id ("" for no device)
//	KEYS[4] device key           ARGV[4] device name
//	KEYS[5] device wrapped key   ARGV[5] device public key
//	                             ARGV[6] wrapped group key
//	                             ARGV[7] "1" when ARGV[6] is present
var createGroupScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 1 end
if redis.call('HGET', KEYS[1], 'used') == '1' then return 2 end

redis.call('HSET', KEYS[1], 'used', '1', 'group_id', ARGV[1])
redis.call('HSET', KEYS[2], 'epoch', '1', 'created_at', ARGV[2])

if ARGV[3] ~= '' then
  redis.call('SADD', KEYS[3], ARGV[3])
  redis.call('HSET', KEYS[4],
    'group_id', ARGV[1],
    'name', ARGV[4],
    'pubkey', ARGV[5],
    'created_at', ARGV[2],
    'last_seen', '0')
  if ARGV[7] == '1' then
    redis.call('HSET', KEYS[5], 'epoch', '1', 'key', ARGV[6])
  end
end

return 0
`)

// applyRekeyScript is SPEC §3.3 step 4: write every wrapped key, delete the
// revoked device, increment the epoch — or change nothing at all.
//
// Return codes: 0 applied, 1 no such group, 2 epoch conflict, 3 the revoked
// device is not a member, 4 the wrapped keys do not cover exactly the
// remaining devices.
//
//	KEYS[1] group key            ARGV[1] revoked device id
//	KEYS[2] group devices set    ARGV[2] expected epoch
//	                             ARGV[3] device key prefix
//	                             ARGV[4] wrapped key suffix
//	                             ARGV[5..] device id, wrapped key, ...
//
// The write order matters and is asserted by TestApplyRekeyPartialApply: the
// epoch is bumped last, so an interrupted apply leaves the group at its old
// epoch — the failure mode SPEC §3.3 step 4 demands.
const applyRekeyBody = `
if redis.call('EXISTS', KEYS[1]) == 0 then return 1 end
if redis.call('HGET', KEYS[1], 'epoch') ~= ARGV[2] then return 2 end
if redis.call('SISMEMBER', KEYS[2], ARGV[1]) == 0 then return 3 end

local remaining = {}
local remaining_n = 0
for _, id in ipairs(redis.call('SMEMBERS', KEYS[2])) do
  if id ~= ARGV[1] then
    remaining[id] = true
    remaining_n = remaining_n + 1
  end
end

local supplied_n = 0
for i = 5, #ARGV, 2 do
  local id = ARGV[i]
  if remaining[id] ~= true then return 4 end
  remaining[id] = 'seen'
  supplied_n = supplied_n + 1
end
if supplied_n ~= remaining_n then return 4 end

local next_epoch = tostring(tonumber(ARGV[2]) + 1)
for i = 5, #ARGV, 2 do
  redis.call('HSET', ARGV[3] .. ARGV[i] .. ARGV[4], 'epoch', next_epoch, 'key', ARGV[i + 1])
end
-- Test seam: scripts_test.go swaps the next line for an error() to prove that
-- an apply interrupted here leaves the group at its old epoch. Nothing below
-- this point has run at that moment, which is the whole reason the epoch is
-- bumped last.
-- FAILPOINT
redis.call('SREM', KEYS[2], ARGV[1])
redis.call('DEL', ARGV[3] .. ARGV[1])
redis.call('DEL', ARGV[3] .. ARGV[1] .. ARGV[4])
redis.call('HSET', KEYS[1], 'epoch', next_epoch)

return 0
`

var applyRekeyScript = redis.NewScript(applyRekeyBody)

// consumePairingScript binds a pairing to exactly one joining device
// (SPEC §3.2). Return codes: 0 consumed, 1 no such pairing (or expired),
// 2 already consumed.
//
//	KEYS[1] pairing key          ARGV[1] joiner device id
var consumePairingScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 1 end
if redis.call('HGET', KEYS[1], 'joiner_device_id') ~= false then return 2 end
redis.call('HSET', KEYS[1], 'joiner_device_id', ARGV[1])
return 0
`)
