package exec

import (
	"context"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Reclaim holds again a child this process spawned before an execve kept
// it — a supervisor's hand-over (kitsunium/sdk#738) —: a pidfd and the
// claim on its exit status (ADR 0093), so Wait reads its real status and
// reaps it, as Start's handle does, and the reaper hands it the status a
// sweep took. The child must be this process's, read from
// /proc/<pid>/stat before and after the claim: a pid gone, reaped already
// or another's is AdoptionRefused, a pid below 1 InvalidSpec. Its process
// group is its own when it leads one. Linux only; UnsupportedPlatform
// elsewhere. ctx is read before any kernel call.
func Reclaim(ctx context.Context, pid int) (proc coreproc.Process, err error) {
	//: a cancelled context short-circuits before touching the OS.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the cancellation verbatim.
		return nil, ctxErr
	}
	//: no process has a pid below 1.
	if pid < 1 {
		//: the bare sentinel: a contract violation.
		return nil, coreproc.InvalidSpec
	}
	//: the platform's claim.
	return reclaimChild(pid)
}
