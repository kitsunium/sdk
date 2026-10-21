package failover

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// Config tunes a failover Sink at construction time. The zero value is New's
// behaviour: every branch tried in declaration order on every Write.
type Config struct {
	// Cooldown is how long a branch whose Write failed is tried last rather
	// than in its place. Non-positive disables it — every Write walks the
	// declaration order, as New does. A branch that accepts a Write is back in
	// its place at once.
	Cooldown time.Duration
	// Clock measures Cooldown; nil falls back to clock.System. It is read once
	// per Write, and only when Cooldown is set.
	Clock clock.Clock
}
