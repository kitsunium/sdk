// Package async: policy.go declares the DropPolicy enum used by the async
// Sink to decide what happens when the ring buffer saturates. Pulled into
// its own file so async_sink.go stays focused on the Sink contract.
package async

// DropPolicy selects how a saturated ring buffer behaves on Write.
type DropPolicy uint8

// DropNewest discards the new entry when the ring is full and surfaces
// BufferFull to the caller. The OnDrop callback fires for the dropped entry.
const DropNewest DropPolicy = 0

// DropOldest discards the oldest queued entry to make room for the new one.
// The OnDrop callback fires for the dropped entry; Write returns nil.
const DropOldest DropPolicy = 1
