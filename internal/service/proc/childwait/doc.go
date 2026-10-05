// Package childwait decides who receives a child's exit status (ADR 0093).
//
// A process has two kinds of waiter for its children. The Process handle
// returned by service/proc/exec waits for the one child it spawned. The reaper
// (service/proc/reaper), switched on when the supervisor runs as pid 1 or as a
// subreaper, answers every SIGCHLD with wait4(-1) — which collects ANY child,
// the handle's included. The kernel hands a zombie's status to exactly one
// wait, so before this package whichever call reached the kernel first kept it:
// when the sweep won, the handle's own wait failed with ECHILD and an exit 0
// was reported as WAIT_FAILED. A supervisor read that as a failure and
// restarted a service that had stopped cleanly.
//
// The ledger makes every status reach its owner whoever collects it:
//
//   - Spawn forks and CLAIMS the child in one step. The fork runs under a
//     shared gate and the pid is claimed before the gate is released, so no
//     sweep can decide a child is nobody's while its spawn is in flight.
//   - ReapAny is the only wait4(-1) in the SDK. A child it collects that is
//     claimed has its status stored on the claim, under a lock held from before
//     the wait4 until after the hand-off. The claim is looked up only once no
//     spawn is between fork and claim, so a child that exited before its spawn
//     registered it is found, and a claim left on a recycled pid has already
//     been replaced by the newer child's. A child nobody claims is an orphan.
//   - The owner still waits for its own child itself, so a process without a
//     reaper behaves exactly as before. When that wait fails with ECHILD, the
//     owner takes the sweep lock — any hand-off still in progress lands first —
//     and reads the status from its claim. A status stored before the owner
//     ever waited is read first, so it is never waited for at all.
//
// A status is lost only when something outside the SDK reaps the child, and
// the owner then learns it deterministically instead of hanging.
//
// Package childwait — off Unix there is no wait4 and no reaper, so nothing but
// the owner ever collects a child and the ledger only records claims.
//
// Package childwait — the Unix half: the status wait4 reports, and the one
// wait4 for any child in the SDK (wait4(-1); wait4(0) on illumos and Solaris,
// see waitany_solaris.go).
//
// Package childwait — the pid that means "any child" to wait4 on illumos and
// Solaris (the solaris build tag selects both).
//
// Package childwait — the pid that means "any child" to wait4 on every Unix but
// illumos and Solaris, whose libc reads it otherwise (waitany_solaris.go).
package childwait
