// Package cache — the NewChain constructor configuration.
package cache

// ChainConfig parameterises [NewChain]. Its zero value builds a working chain;
// the one field is an observation hook, not a policy.
type ChainConfig struct {
	// OnPromoteError, if non-nil, is called when a value served from a FAR
	// tier could not be written into a nearer one.
	//
	// A failed promotion is not a failed read: the caller already has the
	// value, and failing the call would turn a degraded cache into a degraded
	// service. But it is also not nothing — a near tier that rejects every
	// promotion is a tier that will never be used again, and with this hook nil
	// that is completely invisible. Wire it.
	OnPromoteError func(key string, err error)
}
