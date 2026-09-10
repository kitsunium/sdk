// Package sql — hosts the validated, clamped form of a Config.
package sql

import (
	stdsql "database/sql"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// resolved is the validated, clamped form of a Config. Constructors work from
// this so the clamps are applied in exactly one place.
type resolved struct {
	db      *stdsql.DB
	dialect coresql.Dialect
	clk     clock.Timed
	probe   time.Duration
}
