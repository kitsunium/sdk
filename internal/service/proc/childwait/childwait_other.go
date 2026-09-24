//go:build !unix

// Package childwait — off Unix there is no wait4 and no reaper, so nothing but
// the owner ever collects a child and the ledger only records claims.
package childwait

// StatusValue carries nothing off Unix: no sweep exists to collect a child, so
// no claim is ever filled and an owner always has the status from its own wait.
type StatusValue struct{}
