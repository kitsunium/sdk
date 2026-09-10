// Package health — hosts ReadinessCheckValue, the only registration that can
// express a dependency. See health_check.go for the table of what each of the
// three may say.
package health

import "time"

// ReadinessCheckValue registers a check that gates ROUTING: the datastore, the
// broker, the cache, the downstream API. This is where every dependency check
// belongs, and the only registration that can express one.
//
// A readiness failure removes the replica from routing. It never restarts it,
// which is what stops a slow dependency from becoming a restart loop.
//
// The zero value is not registrable and is refused by AddReadiness
// ([InvalidCheck]).
type ReadinessCheckValue struct {
	// Name identifies the check in the report and in every error field. It
	// must be non-empty and unique among readiness checks.
	Name string
	// Check is the body. It is given a context carrying its own budget and
	// MUST honour it: a readiness endpoint that blocks is a second outage on
	// top of the one it was trying to report.
	Check Check
	// Timeout is this check's budget, clamped exactly as
	// [StartupCheckValue.Timeout] is.
	Timeout time.Duration
	// NonCritical downgrades this check's failure from unhealthy to degraded,
	// so the replica KEEPS taking traffic while the check fails.
	//
	// The field is negative — NonCritical rather than Critical — so that its
	// zero value is the strict reading. A `Critical bool` would have made
	// every check somebody forgot to mark into one that can never take a
	// replica out of rotation, which is the failure worth making impossible.
	//
	// Use it for a dependency whose absence degrades the response rather than
	// preventing it: a recommendation service, a metrics sink, a warm cache
	// in front of a datastore that also answers directly.
	NonCritical bool
	// MaxAge caches this check's LAST SUCCESSFUL result for up to that long,
	// so an expensive check is not paid for on every poll.
	//
	// Zero means no cache — every probe runs the check — because a probe that
	// silently reports a measurement nobody asked it to keep is the more
	// surprising of the two behaviours.
	//
	// A value above the registry's ceiling is REFUSED at registration
	// ([StaleCacheWindow]) rather than clamped: the caller asked for a
	// staleness the SDK will not vouch for, and quietly serving a different
	// number is the same lie in a smaller size.
	//
	// Only SUCCESSES are cached. A failure is re-measured on every probe,
	// because the answer an operator needs promptly is the one that says the
	// outage ended.
	MaxAge time.Duration
}
