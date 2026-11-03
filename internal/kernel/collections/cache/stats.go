package cache

// StatsValue is a point-in-time copy of a Cache's counters. Hits/Misses
// count Fetch outcomes; Evictions counts capacity + expiry removals (not
// explicit Deletes).
type StatsValue struct {
	Hits      uint64
	Misses    uint64
	Evictions uint64
	// Weight is the summed weight of the live entries when the cache weighs
	// them — Config.MaxWeight or Config.WeightOf set — and 0 when it does not.
	Weight int64
	// Policy is the eviction policy that runs: Config.Policy, or PolicyLRU
	// for a value the cache does not know.
	Policy Policy
}
