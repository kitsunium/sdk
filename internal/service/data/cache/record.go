// Package cache — what the underlying primitive actually stores.
package cache

import "time"

// record is the value the kernel primitive holds: the caller's payload plus the
// two things the primitive has no concept of.
//
// expireAt is duplicated here even though the primitive tracks its own TTL,
// because FetchEntry must report the REMAINING lifetime and the primitive does
// not expose it. Promoting an entry between tiers with its ORIGINAL TTL would
// refresh it on every move, so an entry that keeps being promoted would never
// expire — a cache that cannot be emptied by time.
//
// tags is the SAME slice the tag index owns, not a copy: one slice means the
// entry and its index entry can never disagree, and the caller's own slice
// never reaches in.
type record[V any] struct {
	value    V
	expireAt time.Time // zero = no deadline
	tags     []string
}
