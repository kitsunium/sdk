// Package lock — hosts the compile-time interface assertions, keeping them out
// of the production source so the runtime binary carries no diagnostic-only
// declarations.
package lock

import corelock "github.com/kitsunium/sdk/internal/core/lock"

// Compile-time assertions that both lockers and both leases still answer every
// contract they claim — and, in one case, that one of them still does NOT.
//
// The negative matters as much as the positives. corelock.Deadliner is how a
// caller asks "can this lock be taken from me while I am running", and the
// answer differs between the two backends: memoryLease has a deadline,
// fileLease does not. If fileLease ever grew a Deadline method by accident,
// nothing would fail to compile and nothing would fail at run time — a caller
// would simply start taking the "this can expire" branch for a lock that
// cannot, and would begin renewing and fencing a lease that needs neither.
// The negative assertion lives in the external test as
// TestFileLeaseIsNotADeadliner, because a `var _ !Deadliner` cannot be
// written; this block names the positives so the pair is greppable together.
var (
	_ corelock.Locker    = (*memoryLocker)(nil)
	_ corelock.Lease     = (*memoryLease)(nil)
	_ corelock.Deadliner = (*memoryLease)(nil)
	_ corelock.Locker    = (*fileLocker)(nil)
	_ corelock.Lease     = (*fileLease)(nil)
)
