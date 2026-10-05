package view

// DefaultMaxBytes is the ceiling [Config.MaxBytes] clamps to when it is not
// positive: 8 MiB.
//
// A rendered HTML document larger than this is a defect or an attack. The value
// is generous by an order of magnitude for a page a browser is meant to parse,
// and it is a CLAMP rather than a refusal because — unlike a lock's TTL, whose
// two readings are opposites (ADR 0031, ADR 0052) — there is a defensible
// universal answer here, and refusing view.Config{FS: templates} would make the
// obvious spelling unusable for no safety gain.
const DefaultMaxBytes int = 8 << 20

// MaxPooledBytes is the largest scratch buffer the engine keeps for reuse.
//
// A single 8 MiB render must not leave 8 MiB pinned in a pool for the life of
// the process; anything above this is returned to the collector instead.
const MaxPooledBytes int = 1 << 20
