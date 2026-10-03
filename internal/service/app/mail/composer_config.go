// Package mail — the composer's optional wiring.
package mail

import (
	"io"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// ComposerConfig configures a [Composer].
//
// Both fields are optional and both CLAMP rather than refuse, because unlike a
// TLS mode there is exactly one defensible value for each (ADR 0031's clamping
// half): the system clock, and crypto/rand.
type ComposerConfig struct {
	// Clock supplies the Date header for a message that carries no explicit
	// one. Nil selects the system clock; a ManualClock makes composition
	// byte-deterministic, which is what the golden tests use.
	Clock clock.Clock
	// Rand supplies the randomness behind multipart boundaries. Nil selects
	// crypto/rand.
	Rand io.Reader
}
