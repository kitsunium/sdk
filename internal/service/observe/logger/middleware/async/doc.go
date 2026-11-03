// Package async wraps any corelogger.Sink with a non-blocking ring buffer
// and a single drainer goroutine, decoupling the producer hot path from
// the (potentially slow) downstream sink. Producers call Write synchronously
// but never block on I/O — entries land in the ring and the drainer sips
// them out. An idle drainer parks on a wake-up signal the next Write sends,
// so a sink with nothing to deliver costs no CPU (#806); with Config.BatchMax
// above one it delivers what it holds to a corelogger.BatchSink downstream in
// one call (#807).
//
// When the ring saturates, the configured DropPolicy decides:
//   - DropNewest: the new Write returns BufferFull; the OnDrop callback fires.
//   - DropOldest: the oldest queued entry is silently discarded; the new
//     entry takes its slot. OnDrop fires for the dropped entry.
//   - Block: the Write waits for a free slot, at most MaxBlock on the
//     configured Clock; past it the entry is dropped, OnDrop fires and Write
//     returns BlockTimeout.
//
// Use case: wrap CloudWatch / S3 / HTTP sinks so a slow remote drain never
// stalls the application's hot path.
//
// Package async — declares the DropPolicy enum used by the async Sink to
// decide what happens when the ring buffer saturates. Held as a sibling of
// async_sink.go so the sink file stays focused on the Sink contract.
//
// Package async — holds the Config struct consumed by New. Pulled
// into its own file so async_sink.go stays focused on the Sink contract.
//
// Package async — declares the goroutine loop that consumes the
// ring buffer and forwards entries to the downstream sink. Pulled into its
// own file so async_sink.go stays focused on the Sink contract.
//
// Package async — holds the recycled payload struct that travels
// through the ring buffer between the producer (Write) and the consumer
// (drain goroutine).
//
// Package async — gathers the small runtime helpers used by the
// async drainer (channel-closed probe, background context, error swallowers).
// Pulled out of async_sink.go to keep that file focused on the Sink contract.
package async
