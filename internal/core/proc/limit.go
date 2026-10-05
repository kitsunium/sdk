package proc

// LimitInfinity is the soft/hard value meaning "no limit" (RLIM_INFINITY). Use
// it in a LimitValue to lift a resource ceiling rather than setting a number.
const LimitInfinity uint64 = ^uint64(0)
