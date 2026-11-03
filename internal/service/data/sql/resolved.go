package sql

import (
	stdsql "database/sql"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// resolved is the validated, clamped form of a Config. Constructors work from
// this so the clamps are applied in exactly one place.
type resolved struct {
	db *stdsql.DB
	// replica is the read replica's pool, nil when none was configured.
	replica *stdsql.DB
	dialect coresql.Dialect
	clk     clock.Timed
	probe   time.Duration
	// budget bounds each statement the transactor's executors run.
	budget stmtBudget
	// primary and reader are the executors handed out outside a transaction:
	// the primary's pool and the one reads are routed to, each bounded by
	// the budget when one is set — built once, so Join stays one load.
	primary, reader coresql.Executor
}
