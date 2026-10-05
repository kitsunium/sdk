// Package failover implements a sequential-retry Sink that tries downstream
// sinks in order until one succeeds. The first successful Write returns
// nil; if every sink fails, Write returns Exhausted wrapping an
// errors.Join of the per-sink failures.
//
// Use case: emit logs to a primary collector with a local file as a
// fallback so logs survive a network outage.
package failover
