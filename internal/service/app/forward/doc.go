// Package forward is the store-and-forward engine (ADR 0185): a batch kept in
// a durable queue by Enqueue, sent later by Run through the caller's Sender,
// retried by resilience's retry on the injected clock — the delay a collector
// imposes honoured in full, the backoff otherwise, the batch's age as the
// budget —, dead-lettered when the sender refuses it for good, and counted at
// every step. The queue is bounded in batches and bytes, and a full one drops
// its oldest batch, refuses the newest, or makes the producer wait a bounded
// time. NewSpanExporter and NewMetricExporter put a signal's batch in, encoded
// once as the OTLP/JSON request its OTLP/HTTP exporter posts.
//
// Code range 0.3.93.*, declared in internal/core/app/forward (ADR 0160).
package forward
