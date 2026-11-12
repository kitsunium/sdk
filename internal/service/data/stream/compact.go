package stream

import (
	"math"
	"time"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// compactLines are the two instants one compaction judges records by, the
// same on every engine: a record appended at or before old is at least
// CompactAfter old and may be dropped; a tombstone appended at or before
// tomb is at least twice CompactAfter old, past the window it is kept for.
type compactLines struct {
	old  time.Time
	tomb time.Time
}

// compactLinesAt returns the lines of a compaction at now under after, a
// positive CompactAfter. Twice a CompactAfter past the range of a duration
// is read as the longest one, so a tombstone is then kept for good.
func compactLinesAt(now time.Time, after time.Duration) compactLines {
	twice := time.Duration(math.MaxInt64)
	//: doubled where it fits.
	if after <= math.MaxInt64/2 {
		twice = 2 * after
	}
	//: both measured back from the same instant.
	return compactLines{old: now.Add(-after), tomb: now.Add(-twice)}
}

// compactable reports whether a record appended at at is old enough to be
// compacted.
func (c compactLines) compactable(at time.Time) bool {
	//: at least CompactAfter old.
	return !at.After(c.old)
}

// drops reports whether a compactable record appended at at goes: a keyed
// record a later compactable record of its key supersedes (last false), or
// the last one of its key when it is a tombstone past its second window. An
// unkeyed record never goes.
func (c compactLines) drops(record corestream.RecordValue, at time.Time, last bool) bool {
	//: compaction is by key, and an unkeyed record has none.
	if len(record.Key) == 0 {
		//: kept.
		return false
	}
	//: superseded.
	if !last {
		//: dropped.
		return true
	}
	//: a tombstone, kept one more window, then dropped.
	return len(record.Payload) == 0 && !at.After(c.tomb)
}

// compactResult builds the value a compaction reports.
func compactResult(scanned, dropped int64) corestream.CompactValue {
	//: Kept and Dropped add up to Scanned.
	return corestream.CompactValue{Scanned: scanned, Kept: scanned - dropped, Dropped: dropped}
}

// noteLast records at as the last place a compaction saw key. An unkeyed
// record is nobody's last, so an empty key records nothing. It converts the
// key inside the map index, where the compiler spares the copy a lookup
// does not need.
func noteLast[V any](last map[string]V, key []byte, at V) {
	//: an unkeyed record is nobody's last.
	if len(key) > 0 {
		last[string(key)] = at
	}
}

// lastOf is the last place a compaction saw key, the zero V when it saw none.
func lastOf[V any](last map[string]V, key []byte) V {
	//: the lookup converts in place: no copy of the key.
	return last[string(key)]
}
