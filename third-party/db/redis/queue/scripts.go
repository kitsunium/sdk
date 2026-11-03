package queue

import goredis "github.com/redis/go-redis/v9"

// The keys every script receives, in this order: the stream, the leases
// (a sorted set of entry ids by deadline), the waiting hand-backs (by the
// instant they become visible), the delivery counts and the lease tokens (two
// hashes by entry id), and the dead-letter stream. Every instant is
// microseconds on the broker's clock, passed in: the server's clock decides
// nothing.

// deadLetterLua moves one entry to the dead-letter stream with its cause and
// removes every trace of it, in the calling script.
const deadLetterLua = `
local function deadLetter(group, id, fields, count, reason, cause, code, trail, failed)
  local record = {'id', id, 'n', count, 'f', failed, 'r', reason, 'c', cause, 'k', code, 't', trail}
  for i = 1, #fields do record[#record + 1] = fields[i] end
  redis.call('XADD', KEYS[6], '*', unpack(record))
  redis.call('XACK', KEYS[1], group, id)
  redis.call('XDEL', KEYS[1], id)
  redis.call('ZREM', KEYS[2], id)
  redis.call('ZREM', KEYS[3], id)
  redis.call('HDEL', KEYS[4], id)
  redis.call('HDEL', KEYS[5], id)
end
`

var (
	// receiveScript leases up to max entries: first the lapsed leases are
	// reclaimed — each dead-lettered when its deliveries are spent, waiting
	// otherwise —, then the waiting entries whose instant has come are leased
	// again, oldest first, then new entries are read through the group.
	//
	// ARGV: now, the new leases' deadline, max, group, consumer, max deliveries,
	// token base, reclaim limit. Returns 'NOGROUP', or {id, fields, deliveries, token}…
	receiveScript = goredis.NewScript(deadLetterLua + `
local now, deadline, max = ARGV[1], ARGV[2], tonumber(ARGV[3])
local group, consumer, maxd, base = ARGV[4], ARGV[5], tonumber(ARGV[6]), ARGV[7]
local groups = redis.pcall('XINFO', 'GROUPS', KEYS[1])
if type(groups) ~= 'table' or groups.err then return 'NOGROUP' end
local known = false
for _, info in ipairs(groups) do
  for i = 1, #info, 2 do
    if info[i] == 'name' and info[i + 1] == group then known = true end
  end
end
if not known then return 'NOGROUP' end
local lapsed = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now, 'WITHSCORES', 'LIMIT', 0, tonumber(ARGV[8]))
for i = 1, #lapsed, 2 do
  local id, lapsedAt = lapsed[i], lapsed[i + 1]
  redis.call('ZREM', KEYS[2], id)
  redis.call('HDEL', KEYS[5], id)
  local entry = redis.call('XRANGE', KEYS[1], id, id)
  local count = tonumber(redis.call('HGET', KEYS[4], id) or '0')
  if #entry == 0 then
    redis.call('HDEL', KEYS[4], id)
  elseif count >= maxd then
    deadLetter(group, id, entry[1][2], count, 'LEASE_EXPIRED', '', 0, '', now)
  else
    redis.call('ZADD', KEYS[3], lapsedAt, id)
  end
end
local out, n = {}, 0
local function grant(id, fields)
  n = n + 1
  local count = redis.call('HINCRBY', KEYS[4], id, 1)
  local token = base .. n
  redis.call('HSET', KEYS[5], id, token)
  redis.call('ZADD', KEYS[2], deadline, id)
  out[#out + 1] = {id, fields, count, token}
end
local ready = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', now, 'LIMIT', 0, max)
for _, id in ipairs(ready) do
  redis.call('ZREM', KEYS[3], id)
  local entry = redis.call('XRANGE', KEYS[1], id, id)
  if #entry == 0 then
    redis.call('HDEL', KEYS[4], id)
  else
    redis.call('XCLAIM', KEYS[1], group, consumer, 0, id, 'JUSTID')
    grant(id, entry[1][2])
  end
end
if n < max then
  local read = redis.call('XREADGROUP', 'GROUP', group, consumer, 'COUNT', max - n, 'STREAMS', KEYS[1], '>')
  if read then
    for _, entry in ipairs(read[1][2]) do grant(entry[1], entry[2]) end
  end
end
return out
`)

	// settleScript settles one lease: ack, nack, reject, release or extend. The
	// lease must still be the receipt's — the same token, a deadline still ahead
	// — or nothing moves.
	//
	// ARGV: op, id, token, now, group, then per op: nack — the instant it
	// becomes visible again, max deliveries, reason, cause, code, trail;
	// reject — reason, cause, code, trail; extend — the new deadline, the new
	// token. Returns 'GONE', 'REPLACED', 'LAPSED', 'OK',
	// 'BACK' or 'DEAD', with the deliveries after it.
	settleScript = goredis.NewScript(deadLetterLua + `
local op, id, token, now, group = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5]
local held = redis.call('HGET', KEYS[5], id)
if not held then return {'GONE', 0} end
if held ~= token then return {'REPLACED', 0} end
local deadline = redis.call('ZSCORE', KEYS[2], id)
if not deadline or tonumber(deadline) <= tonumber(now) then return {'LAPSED', 0} end
local count = tonumber(redis.call('HGET', KEYS[4], id) or '0')
local function fieldsOf()
  local entry = redis.call('XRANGE', KEYS[1], id, id)
  if #entry == 0 then return {} end
  return entry[1][2]
end
if op == 'ack' then
  redis.call('XACK', KEYS[1], group, id)
  redis.call('XDEL', KEYS[1], id)
  redis.call('ZREM', KEYS[2], id)
  redis.call('HDEL', KEYS[4], id)
  redis.call('HDEL', KEYS[5], id)
  return {'OK', count}
elseif op == 'nack' then
  if count >= tonumber(ARGV[7]) then
    deadLetter(group, id, fieldsOf(), count, ARGV[8], ARGV[9], ARGV[10], ARGV[11], now)
    return {'DEAD', count}
  end
  redis.call('ZREM', KEYS[2], id)
  redis.call('HDEL', KEYS[5], id)
  redis.call('ZADD', KEYS[3], ARGV[6], id)
  return {'BACK', count}
elseif op == 'reject' then
  deadLetter(group, id, fieldsOf(), count, ARGV[6], ARGV[7], ARGV[8], ARGV[9], now)
  return {'DEAD', count}
elseif op == 'release' then
  redis.call('HINCRBY', KEYS[4], id, -1)
  redis.call('ZREM', KEYS[2], id)
  redis.call('HDEL', KEYS[5], id)
  redis.call('ZADD', KEYS[3], now, id)
  return {'BACK', count - 1}
elseif op == 'extend' then
  redis.call('ZADD', KEYS[2], ARGV[6], id)
  redis.call('HSET', KEYS[5], id, ARGV[7])
  return {'OK', count}
end
return redis.error_reply('unknown op')
`)
)
