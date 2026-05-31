// Package encwrite — declares the Config value type consumed by
// NewEncWriter. Kept in its own file per the one-exported-struct rule.
package encwrite

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// Config configures an EncWriter: the downstream sink, the master key the
// per-sink subkey is derived from, and the HKDF info label that binds that
// subkey to a purpose.
type Config struct {
	// Sink is the downstream destination for framed sealed boxes.
	Sink corelogger.Sink
	// Key is the master key from which a per-sink subkey is derived.
	Key corecrypto.Key
	// Info is the HKDF info label for per-sink domain separation; empty falls
	// back to a package default that binds the subkey to the sink purpose.
	Info string
}
