// Package async: async_sink_policy.go declares the DropPolicy enum used
// by the async Sink to decide what happens when the ring buffer saturates.
// Held as a parent-prefixed sibling of async_sink.go so the sink file stays
// focused on the Sink contract while preserving the KTN-STRUCT-COLOCATE
// convention.
package async

// DropPolicy selects how a saturated ring buffer behaves on Write.
type DropPolicy uint8

// DropNewest discards the new entry when the ring is full and surfaces
// BufferFull to the caller. The OnDrop callback fires for the dropped entry.
const DropNewest DropPolicy = 0

// DropOldest discards the oldest queued entry to make room for the new one.
// The OnDrop callback fires for the dropped entry; Write returns nil.
const DropOldest DropPolicy = 1
