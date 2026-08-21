// Package cache — the read-only counters snapshot.
package cache

// StatsValue is a point-in-time copy of a Cache's counters. Hits/Misses
// count Fetch outcomes; Evictions counts capacity + expiry removals (not
// explicit Deletes).
type StatsValue struct {
	Hits      uint64
	Misses    uint64
	Evictions uint64
}
