package cache

// record is the value the kernel primitive holds: the caller's payload plus the
// things the primitive has no concept of. It holds nothing the primitive
// already keeps (#1059): the deadline is the primitive's, read back through
// UnlockedMap.Deadline when FetchEntry or LoadRefreshing needs what is left
// of it, and the weight is the primitive's, measured once at Set and handed
// over through the stripe's write in progress (memoryStripe.weightOf). For a
// string value the record is seven words, so a hit returns it, and its found
// flag, in registers at every level (the amd64 ABI's nine result registers);
// with the deadline and the weight it was eleven, copied through memory three
// times on every hit.
//
// FetchEntry still reports the REMAINING lifetime, not the original: promoting
// an entry between tiers with its ORIGINAL TTL would refresh it on every move,
// so an entry that keeps being promoted would never expire — a cache that
// cannot be emptied by time.
//
// tags is the SAME slice the tag index owns, not a copy: one slice means the
// entry and its index entry can never disagree, and the caller's own slice
// never reaches in.
//
// absent is non-nil for a NEGATIVE entry (#838): a fill answered with
// corecache.Absent, and the store keeps that answer instead of a value. Fetch
// reports it as a miss; Load returns it without calling a fill.
type record[V any] struct {
	value  V
	tags   []string
	absent error
}
