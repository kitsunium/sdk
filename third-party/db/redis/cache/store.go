package cache

import (
	"context"
	"errors"
	"slices"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/concur/singleflight"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
)

// scanPage is how many members one SSCAN of a tag's group asks for: the
// size of one invalidation script, so no script holds the server long.
const scanPage int64 = 128

// redisStore is the store NewStore returns.
type redisStore[V any] struct {
	client     goredis.UniversalClient
	codec      codec.Codec
	clock      clock.Timed
	group      singleflight.Group[string, V]
	keys       keySpace
	defaultTTL time.Duration
	fillLease  time.Duration
	fillPoll   time.Duration
}

// NewStore returns the store over cfg.Client under cfg.Prefix. It sends no
// command. A missing client or codec, an empty prefix or one holding a
// brace, a negative DefaultTTL, and a FillPoll that is not positive and
// shorter than a positive FillLease are core/data/cache.CacheMisconfigured.
func NewStore[V any](cfg Config) (store corecache.Store[V], err error) {
	//: everything decidable without the server is decided here.
	if verr := cfg.validate(); verr != nil {
		//: nothing built.
		return nil, verr
	}
	//: no command sent.
	return &redisStore[V]{
		client: cfg.Client, codec: cfg.Codec, clock: cfg.clockOrSystem(), keys: newKeySpace(cfg.Prefix),
		defaultTTL: cfg.DefaultTTL, fillLease: cfg.FillLease, fillPoll: cfg.FillPoll,
	}, nil
}

// Fetch returns the value under key: one GET.
func (s *redisStore[V]) Fetch(ctx context.Context, key string) (value V, found bool, err error) {
	data, gerr := s.client.Get(ctx, s.keys.value(key)).Bytes()
	//: a miss is not an error.
	if errors.Is(gerr, goredis.Nil) {
		//: zero, false, nil.
		return value, false, nil
	}
	//: the server failed.
	if gerr != nil {
		//: COMMAND_FAILED.
		return value, false, commandFailed(gerr, "GET")
	}
	//: the codec's verdict.
	return s.decode(data)
}

// FetchEntry returns the entry under key whole: value, remaining TTL, tags.
func (s *redisStore[V]) FetchEntry(ctx context.Context, key string) (entry corecache.EntryValue[V], found bool, err error) {
	reply, rerr := fetchScript.Run(ctx, s.client, []string{s.keys.value(key), s.keys.tags(key)}).Slice()
	//: a miss is not an error.
	if errors.Is(rerr, goredis.Nil) {
		//: zero, false, nil.
		return entry, false, nil
	}
	//: the server failed, or answered what the script never returns.
	if rerr != nil || len(reply) != 3 {
		//: COMMAND_FAILED.
		return entry, false, commandFailed(rerr, "FETCH")
	}
	raw, _ := reply[0].(string)
	value, _, derr := s.decode([]byte(raw))
	//: a value another writer left.
	if derr != nil {
		//: VALUE_UNDECODABLE.
		return entry, false, derr
	}
	millis, _ := reply[1].(int64)
	//: the REMAINING time, as the chain requires, never the first TTL.
	return corecache.EntryValue[V]{Value: value, TTL: remaining(millis), Tags: tagsOf(reply[2])}, true, nil
}

// remaining is the TTL PTTL's answer is: NoExpiry for none.
func remaining(millis int64) time.Duration {
	//: -1 is a key with no deadline.
	if millis < 0 {
		//: the domain's spelling of none.
		return corecache.NoExpiry
	}
	//: what is left, at least a millisecond.
	return max(time.Duration(millis)*time.Millisecond, time.Millisecond)
}

// tagsOf reads the tag set a script returned, sorted.
func tagsOf(reply any) []string {
	var members []any
	//: a script's array reply, else no tag at all.
	switch list := reply.(type) {
	case []any:
		members = list
	default:
		//: not a set this store wrote: nil, as for none.
		return nil
	}
	//: no tag: nil, as the memory store returns.
	if len(members) == 0 {
		return nil
	}
	tags := make([]string, 0, len(members))
	//: each member a string.
	for _, member := range members {
		//: anything else is not a tag this store wrote.
		if tag, ok := member.(string); ok {
			tags = append(tags, tag)
		}
	}
	slices.Sort(tags)
	//: order is not significant; sorted reads the same twice.
	return tags
}

// decode reads a stored value with the store's codec.
func (s *redisStore[V]) decode(data []byte) (value V, found bool, err error) {
	//: a value the codec cannot read is not a miss.
	if uerr := s.codec.Unmarshal(data, &value); uerr != nil {
		var zero V
		//: VALUE_UNDECODABLE.
		return zero, false, undecodable(uerr, s.codec.Name())
	}
	//: found.
	return value, true, nil
}

// Set stores entry under key: one script.
func (s *redisStore[V]) Set(ctx context.Context, key string, entry corecache.EntryValue[V]) error {
	//: the memory store's refusals, before any command.
	if verr := validateEntry(key, entry); verr != nil {
		//: CACHE_ENTRY_REJECTED.
		return verr
	}
	data, merr := s.codec.Marshal(entry.Value)
	//: a value the codec cannot write is the caller's.
	if merr != nil {
		//: CACHE_ENTRY_REJECTED, the codec's error kept.
		return rejected("value", merr.Error())
	}
	args := make([]any, 0, 4+len(entry.Tags))
	args = append(args, data, s.ttlMillis(entry.TTL), s.keys.group(""), key)
	//: the tags, each once.
	for _, tag := range uniqueTags(entry.Tags) {
		args = append(args, tag)
	}
	//: atomic on the server: value, deadline and tag index together.
	if rerr := setScript.Run(ctx, s.client, []string{s.keys.value(key), s.keys.tags(key)}, args...).Err(); rerr != nil {
		//: COMMAND_FAILED.
		return commandFailed(rerr, "SET")
	}
	//: stored.
	return nil
}

// ttlMillis maps the domain's three TTL meanings onto PX: 0 is no deadline.
func (s *redisStore[V]) ttlMillis(ttl time.Duration) int64 {
	//: an explicit "no deadline".
	if ttl == corecache.NoExpiry {
		return 0
	}
	//: the unstated lifetime is the store's, itself possibly none.
	if ttl == 0 {
		ttl = s.defaultTTL
	}
	//: none.
	if ttl <= 0 {
		return 0
	}
	//: rounded up: PX counts milliseconds, and zero would mean none.
	return int64((ttl + time.Millisecond - 1) / time.Millisecond)
}

// Delete removes key: one script, idempotent.
func (s *redisStore[V]) Delete(ctx context.Context, key string) error {
	keys := []string{s.keys.value(key), s.keys.tags(key)}
	//: the entry and its place in its tags' groups.
	if rerr := deleteScript.Run(ctx, s.client, keys, s.keys.group(""), key).Err(); rerr != nil {
		//: COMMAND_FAILED.
		return commandFailed(rerr, "DEL")
	}
	//: gone, or never there.
	return nil
}

// InvalidateTag removes every entry carrying tag: SSCAN of the tag's group,
// one script per page.
func (s *redisStore[V]) InvalidateTag(ctx context.Context, tag string) (removed int, err error) {
	group := s.keys.group(tag)
	var cursor uint64
	//: page by page until the cursor comes back to zero.
	for {
		members, next, serr := s.client.SScan(ctx, group, cursor, "", scanPage).Result()
		//: the server failed: what was removed stays removed.
		if serr != nil {
			//: COMMAND_FAILED, the count so far reported.
			return removed, commandFailed(serr, "SSCAN")
		}
		count, perr := s.invalidatePage(ctx, group, members)
		removed += count
		//: a failed page.
		if perr != nil {
			//: COMMAND_FAILED.
			return removed, perr
		}
		cursor = next
		//: the scan is complete.
		if cursor == 0 {
			return removed, nil
		}
	}
}

// invalidatePage removes one page of a group's members.
func (s *redisStore[V]) invalidatePage(ctx context.Context, group string, members []string) (removed int, err error) {
	//: an empty page costs no script.
	if len(members) == 0 {
		return 0, nil
	}
	args := make([]any, 0, 1+len(members))
	args = append(args, s.keys.base)
	//: each member, as the group holds it.
	for _, member := range members {
		args = append(args, member)
	}
	count, rerr := invalidateScript.Run(ctx, s.client, []string{group}, args...).Int()
	//: the server failed.
	if rerr != nil {
		//: COMMAND_FAILED.
		return 0, commandFailed(rerr, "INVALIDATE")
	}
	//: the entries whose value was there.
	return count, nil
}

// validateEntry refuses what the memory store refuses, identically.
func validateEntry[V any](key string, entry corecache.EntryValue[V]) error {
	//: an empty key names nothing a caller can ask for.
	if key == "" {
		return rejected("key", "empty")
	}
	//: an empty tag names a set nobody can ask for.
	if slices.Contains(entry.Tags, "") {
		return rejected("tag", "empty")
	}
	//: NoExpiry is the one negative TTL with a meaning.
	if entry.TTL < 0 && entry.TTL != corecache.NoExpiry {
		return rejected("ttl", entry.TTL.String())
	}
	//: storable.
	return nil
}

// uniqueTags is tags with duplicates collapsed, the caller's slice untouched.
func uniqueTags(tags []string) []string {
	unique := slices.Clone(tags)
	slices.Sort(unique)
	//: order is not significant.
	return slices.Compact(unique)
}
