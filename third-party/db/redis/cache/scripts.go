package cache

import goredis "github.com/redis/go-redis/v9"

var (
	// setScript writes one entry: its value with its deadline, its tag set, and
	// its key in each tag's group, after removing it from the groups of the tags
	// it carried before. A group lives at least as long as its longest entry, so
	// a group whose entries all expired expires with them.
	//
	// KEYS: value, tags. ARGV: value, ttl ms (0 none), group prefix, key, tags…
	setScript = goredis.NewScript(`
local old = redis.call('SMEMBERS', KEYS[2])
for _, tag in ipairs(old) do redis.call('SREM', ARGV[3] .. tag, ARGV[4]) end
redis.call('DEL', KEYS[2])
local ttl = tonumber(ARGV[2])
if ttl > 0 then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl)
else
  redis.call('SET', KEYS[1], ARGV[1])
end
for i = 5, #ARGV do
  local group = ARGV[3] .. ARGV[i]
  local existed = redis.call('EXISTS', group)
  redis.call('SADD', group, ARGV[4])
  if ttl > 0 then
    if existed == 0 then
      redis.call('PEXPIRE', group, ttl)
    else
      local left = redis.call('PTTL', group)
      if left >= 0 and left < ttl then redis.call('PEXPIRE', group, ttl) end
    end
  else
    redis.call('PERSIST', group)
  end
  redis.call('SADD', KEYS[2], ARGV[i])
end
if ttl > 0 and #ARGV >= 5 then redis.call('PEXPIRE', KEYS[2], ttl) end
return 1
`)

	// fetchScript reads one entry whole: its value, its remaining time to live in
	// milliseconds (-1 for none) and its tags; nil for a miss.
	//
	// KEYS: value, tags.
	fetchScript = goredis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return false end
local ttl = redis.call('PTTL', KEYS[1])
if ttl == -2 then return false end
return {value, ttl, redis.call('SMEMBERS', KEYS[2])}
`)

	// deleteScript removes one entry and its key from its tags' groups.
	//
	// KEYS: value, tags. ARGV: group prefix, key.
	deleteScript = goredis.NewScript(`
local tags = redis.call('SMEMBERS', KEYS[2])
for _, tag in ipairs(tags) do redis.call('SREM', ARGV[1] .. tag, ARGV[2]) end
return redis.call('DEL', KEYS[1], KEYS[2])
`)

	// invalidateScript removes one page of a tag's members — each entry, its key
	// from every group it is in — and counts the entries whose value was there.
	//
	// KEYS: the tag's group. ARGV: key-space base, key…
	invalidateScript = goredis.NewScript(`
local removed = 0
for i = 2, #ARGV do
  local key = ARGV[i]
  local tagsKey = ARGV[1] .. 't:' .. key
  for _, tag in ipairs(redis.call('SMEMBERS', tagsKey)) do
    redis.call('SREM', ARGV[1] .. 'g:' .. tag, key)
  end
  removed = removed + redis.call('DEL', ARGV[1] .. 'v:' .. key)
  redis.call('DEL', tagsKey)
  redis.call('SREM', KEYS[1], key)
end
return removed
`)

	// releaseScript deletes a fill lease only while it is still the caller's.
	//
	// KEYS: lease. ARGV: token.
	releaseScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0
`)
)
