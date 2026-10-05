// Package sample implements a 1-of-N rate-limiter Sink. Every Nth Write
// is forwarded to the downstream sink; the rest are silently dropped. The
// counter is atomic so concurrent producers stay correct without a mutex.
//
// Use case: emit verbose Debug records at a 1/100 rate to a remote drain
// while keeping the local console stream intact (compose with sink/multi
// from commit 11 for the per-branch policy).
package sample
