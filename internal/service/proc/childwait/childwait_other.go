//go:build !unix

package childwait

// StatusValue carries nothing off Unix: no sweep exists to collect a child, so
// no claim is ever filled and an owner always has the status from its own wait.
type StatusValue struct{}
