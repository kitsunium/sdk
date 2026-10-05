package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Job is scheduled work: a piece of the daemon's internal loop. It runs on
// the SDK scheduler, whose decisions about time are documented rather than
// emergent: a fire that is due while the previous run of the same job is
// still going is skipped and counted, and a missed deadline is skipped and
// counted, never caught up.
type Job = ikit.Job
