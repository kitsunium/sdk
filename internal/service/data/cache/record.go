package cache

import "time"

// record is the value the kernel primitive holds: the caller's payload plus the
// things the primitive has no concept of.
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
//
// absent is non-nil for a NEGATIVE entry (#838): a fill answered with
// corecache.Absent, and the store keeps that answer instead of a value. Fetch
// reports it as a miss; Load returns it without calling a fill.
type record[V any] struct {
	value    V
	expireAt time.Time // zero = no deadline
	tags     []string
	absent   error
	// weight is what the record counts against MaxBytes, measured once at
	// Set; 0 when the store bounds no bytes.
	weight int64
}
