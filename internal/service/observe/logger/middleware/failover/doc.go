// Package failover implements a sequential-retry Sink that tries downstream
// sinks in order until one succeeds. The first successful Write returns
// nil; if every sink fails, Write returns Exhausted wrapping an
// errors.Join of the per-sink failures.
//
// Use case: emit logs to a primary collector with a local file as a
// fallback so logs survive a network outage.
//
// NewWithConfig adds a Cooldown: a branch whose Write failed is tried LAST
// until Cooldown has passed on the configured Clock, so a dead primary stops
// costing every record a failed attempt — a timeout, often — before the
// fallback takes it. A cooling branch is demoted, never skipped: when every
// other branch fails it is still tried, so a cooldown alone never loses a
// record. A zero Cooldown is New's behaviour exactly.
package failover
