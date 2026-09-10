// Package health — hosts the single-flight run that keeps a wedged check from
// becoming a goroutine factory.
package health

import (
	"context"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
)

// inflight is one execution of a check, shared by every probe waiting on it.
//
// It exists so that a check which ignores its cancellation costs ONE goroutine
// for as long as it is wedged, not one per poll. A probe endpoint polled every
// ten seconds against a dependency call with no deadline would otherwise
// accumulate a goroutine — and a connection — every ten seconds, for as long
// as the outage lasts, which is a leak triggered precisely by the thing the
// endpoint was watching for.
type inflight struct {
	// ctx is the body's context, detached from the probe that started it so
	// an HTTP client hanging up does not cancel a run other probes are
	// waiting on. Nil for a liveness body, which takes no context.
	ctx context.Context
	// cancel announces to the body that a budget expired. It is an
	// ANNOUNCEMENT: the registry cannot kill the goroutine and does not try.
	cancel context.CancelFunc
	// done is closed once result is written, and is the only synchronisation
	// a waiter needs. Exactly one goroutine ever closes it — the one
	// [entry.claim] handed `mine` to, under the entry's own lock.
	done chan struct{}
	// result is written exactly once, before done is closed.
	result corehealth.ResultValue
}
