// Package lock — the kernel lock the file locker stands on, and what each
// kernel's version of it means for THIS domain.
//
// The lock itself is internal/kernel/fs/flock: flock(2) on the Unix kernels,
// LockFileEx over every byte of the file on Windows (ADR 0081), and never a
// blocking call — Acquire polls it on the locker's own clock, so its context
// means something. That package states how the two kernels differ. What
// follows is what each difference costs or buys the file locker, which no
// primitive can say on its behalf.
//
// # The gate: load-bearing on Unix, kept on Windows for other reasons
//
// A second lock through the same descriptor is a CONVERSION under flock(2) and
// a REFUSAL under LockFileEx (ADR 0052 §D7 is about the first). So on Windows
// the in-process gate ([nameGate]) is not load-bearing for exclusion — the
// kernel already refuses a second goroutine — and it stays for everything
// else: it keeps one process's goroutines QUEUED on a channel rather than
// polling a range their own process holds; it makes the package behave the
// same on both kernels, so a suite that passes on Linux means something there;
// and it keeps the natural "one handle for the locker's lifetime" refactor
// from turning a silent no-op on Unix into a hard failure on Windows.
//
// # The fencing ledger is inside the locked range
//
// The Windows lock is a byte range, and the kernel primitive takes the whole
// file. That is the choice this domain needs: the ledger lives at offset 0 of
// the very file carrying the lock, and a range that skipped it — one byte at
// an offset nothing will ever occupy — would leave the counter writable by
// every non-holder; TestAHighOffsetRangeLeavesTheLedgerUnprotected measures
// what that alternative gives up. Because the lock is MANDATORY there, the
// ledger is strictly better protected than under flock(2), where nothing stops
// a non-holder from rewriting it, and the holder is exempt per HANDLE, so
// [readFence] and [writeFence] work through the descriptor that placed the
// lock (TestTheLedgerSurfaceWorksThroughTheLockingHandle). The price: a
// foreign `type` of a HELD lock file fails with ERROR_LOCK_VIOLATION, so the
// decimal ledger fence.go writes is readable during an incident only while
// nobody holds the lock — which is the incident it is wanted in, since a
// holder that crashed is a holder whose lock the kernel already released.
//
// # A platform gains the lock and its hardening together, or neither
//
// The lock path is a file, never a link to one (ADR 0082), and that is
// nofollow_*.go's to enforce. A platform with the kernel lock and without the
// hardened open would build a locker any planted link could redirect, so the
// gate below needs both halves, and TestTheLockAndItsHardeningShareAPlatform
// fails the day the kernel's tag set and nofollow_*.go's stop agreeing.
//
// Where neither exists — js, wasip1, plan9, aix, solaris, illumos — the
// constructor returns the typed proc.UnsupportedPlatform and the caller picks
// the in-process locker, an external coordinator, or another host (ADR 0018
// §(a)). The gap is listed in this package's CLAUDE.md §Platform matrix
// rather than left to be discovered.
//
// Package lock — the other half of "the lock path is a file, never a link to
// one": the components ABOVE the lock file.
//
// # O_NOFOLLOW stops at the last component, and that is a kernel limit
//
// [openLockFile] refuses an indirection planted at the lock file's own name.
// It cannot refuse one planted at a PARENT component, because O_NOFOLLOW
// governs the final component only — measured, and recorded in ADR 0082
// §Deferred as the half that stayed open. The whole lock directory moves:
//
//	Dir demandé  = …/pub/myapp/locks
//	composant planté = …/pub/myapp -> …/elsewhere
//	construction ACCEPTÉE
//	acquisition sur parent planté : err=<nil>
//	SUIVI : le verrou a atterri sur …/elsewhere/locks/a4d268….lock
//
// # A link at a parent is not evidence of anything on its own
//
// The obvious remedy — refuse a link anywhere in the path — is wrong, and it
// is wrong on a platform this repository tests on. That is measured rather
// than recited: on the macos-arm64 job of e2e-cross every t.TempDir() resolves
// through /var -> /private/var, which Apple ships. /var/run is one to /run on
// most Linux distributions; C:\Users\All Users is a junction to
// C:\ProgramData. A blanket refusal would turn every lock directory under any
// of them into LOCK_PATH_REDIRECTED, which is ADR 0018 §(a)'s failure mode
// wearing an error that blames the deployment for the operating system's own
// layout.
//
// What separates those from an attack is not the link, it is the directory the
// link LIVES IN. A link in a directory only root can write was put there by
// root. A link in a world-writable directory was put there by anybody at all.
// So the rule is the one ADR 0082 already argued for the final component,
// applied to every component: an indirection is refused when anyone could have
// created it, and the STICKY BIT DOES NOT EXEMPT IT — sticky governs unlinking
// an entry that exists, and planting a component creates one at a name nobody
// has taken.
//
// That is deliberately a different rule from [checkDir]'s, which accepts
// 0777|sticky, and the difference is the same one ADR 0082 turns on:
// checkDir asks who can REPLACE the lock file, this asks who can CREATE a
// component of its path.
//
// Package lock provides the concrete lockers implementing internal/core/app/lock:
// an in-process locker whose leases really expire, and a file locker whose
// leases really do not. Stdlib-only. ADR 0052.
//
// Package lock — the lock directory's safety verdict where a directory's mode
// bits say who may replace its entries (ADR 0081).
//
// Package lock — the lock directory's safety verdict on Windows, where the
// question the POSIX rule asks has no answer and the attack it prevents has no
// mechanism (ADR 0081).
//
// # Two questions, three masks, one reader
//
// Both rules here ask the kernel's DACL reader (internal/kernel/fs/winacl, the
// reader this package wrote for ADR 0084 and that the queue now shares from
// there), and they ask it different things — exactly as the POSIX pair differ
// over the sticky bit, and drawn finer, because Windows spells create and
// delete as separate bits rather than one sticky flag (ADR 0086):
//
//   - [checkDir] asks winacl.ReplaceRights of the lock directory — can a
//     stranger take away the entry a holder created? — and
//     winacl.ContentRights of what the lock FILES created there inherit;
//   - [plantable] asks winacl.CreateRights of the directory holding a path
//     component — can a stranger put a directory at a name nobody has taken?
//
// The reader measures and never decides. What this domain decides on an
// inspection that could not run is to ACCEPT, and to log that it did (see
// [checkDir], [noteUninspected]) — where the queue, asking the same reader,
// refuses (ADR 0095).
//
// Package lock — the on-disk fencing ledger: the one piece of state the file
// locker keeps, and the reason its tokens survive a restart.
//
// Package lock — the file locker: exclusion between PROCESSES on one machine,
// composed with the in-process gate because flock(2) alone provides none
// between goroutines.
//
// Package lock — the file locker's configuration and the two ADR 0031 halves
// it contains: one clamp and one refusal, in one struct, for contrast.
//
// Package lock — the lease handed out by the file locker. It carries no
// deadline, deliberately: see [fileLocker] for why its locks do not expire.
//
// Package lock — the in-process gate the file locker takes BEFORE flock(2),
// because flock alone excludes nothing between goroutines.
//
// Package lock — one live acquisition of one name, in the in-process locker.
//
// Package lock — the lock file's IDENTITY, and the exposure that is detectable
// rather than preventable.
//
// # What was reproduced
//
// In a 0777|sticky directory — /tmp's mode, and a row [checkDir] accepts by
// name — the account that created the lock file OWNS that entry, so the sticky
// bit permits them to unlink it. Not when the lock is free: WHILE THE VICTIM
// HOLDS IT.
//
//	victime détient le verrou : fence=1 inode=69831
//	2e verrou AVANT l'échange : held=false err=<nil> (attendu false)
//	entrée désliée pendant que la victime la détient
//	2e verrou APRÈS l'échange : held=true err=<nil> inode=69832
//	SPLIT : deux détenteurs, fences 1 et 1, inodes 69831 et 69832
//	Extend de la victime : <nil>
//
// Two holders, two inodes, and the fence RESET rather than advanced — both
// report 1. The last line is the one that matters: the victim asked whether it
// still held the lock and was told yes.
//
// # It is detectable, NOT preventable, and the difference is the point
//
// No flag on the open prevents this. The entry is unlinked after the open, by
// an account the directory's permissions genuinely allow to unlink it, and the
// victim's descriptor keeps working because a descriptor outlives its name.
// Three remedies were considered and two of them prevent nothing:
//
//   - An OWNER check on the lock file was rejected in ADR 0082 §Deferred and
//     is still rejected, for the reason given there: it breaks the shared-group
//     arrangement [checkDir] deliberately accepts, where the entry belongs to
//     the OTHER account by design.
//   - O_EXCL on creation plus a recorded identity prevents nothing either, and
//     that was checked rather than assumed. O_EXCL says whether THIS process
//     created the file. It says nothing about who unlinks it afterwards, which
//     is the entire attack — the unlink happens minutes later, on a descriptor
//     that has already been opened and locked.
//   - Comparing the descriptor's identity against the path DETECTS it, which
//     is what ships. The victim finds out at its next [fileLease.Extend], and
//     a Keepalive turns that into a cancelled context for the work inside the
//     section.
//
// So the guarantee is stated as exactly what it is: the lock is not kept, the
// split is not prevented, and the holder is TOLD. A holder that learns it no
// longer holds the lock can stop; a holder that is told it still does cannot.
// The only prevention is a lock directory no other account can write, which is
// what NewFileLocker creates (0700) when the directory is absent.
//
// # Where the check runs, and why not everywhere
//
// At acquisition, between the flock and the fence, because the window between
// the open and the flock is real and closing it is two syscalls. And at
// Extend, because that is the only call a holder makes DURING the section.
//
// Not at Release: releasing is closing a descriptor this lease owns, it
// succeeds whatever the name now points at, and a Release that returned an
// error would break `defer lease.Release(ctx)` at the one moment a caller is
// unwinding.
//
// Package lock — the background renewal, and the only channel through which a
// lost lease can reach work that has already started.
//
// Package lock — hosts the compile-time interface assertions, keeping them out
// of the production source so the runtime binary carries no diagnostic-only
// declarations.
//
// Package lock — the in-process locker: real leases, real expiry, real
// takeover, and therefore the backend where fencing actually matters.
//
// Package lock — the lease handed out by the in-process locker. It is the
// only backend whose leases satisfy corelock.Deadliner, because it is the only
// one whose leases can be taken from a live holder.
//
// Package lock — the lock path must name a FILE, never an indirection to one.
//
// # What this closes
//
// [fileLocker.pathFor] derives the lock file's name from the SHA-256 of the
// lock name, so an attacker cannot choose it — but it is derived, which means
// it is PREDICTABLE, and predictable is all the attacker needs. Create the
// directory first, compute the digest of a lock name the victim will use, and
// plant a symbolic link there. The victim's open follows it, the flock and the
// fencing ledger land on a file of the attacker's choosing, and every layer
// above reports success.
//
// Two locks then become one, or one becomes two, with no error anywhere:
//
//   - The victim's lock lands on the attacker's target while the attacker's own
//     lock lands on the real path. Both processes are inside the section, both
//     hold a lease, neither is blocked, and nothing logs.
//   - The redirected ledger is the attacker's file, so the fencing token the
//     victim hands to the protected resource is a number the attacker CHOSE.
//     Measured: a symlink to a file containing "48213\n" — a pidfile is exactly
//     that shape — made Acquire return fence 48214 and rewrite the pidfile with
//     it.
//
// [checkDir] exists to prevent precisely this substitution, and it does not
// reach it: its rule is about UNLINKING an entry that already exists, while
// this attack creates one at a name nobody has taken yet. The sticky bit is no
// help for the same reason — it stops you removing someone else's entry, and
// the attacker owns the symlink they created. `0777|sticky` is a mode the rule
// explicitly ACCEPTS, and it is what /tmp is.
//
// # It is one gap with two spellings, so it closes on both kernels
//
// The refusal is the same sentinel everywhere, [corelock.LockPathRedirected], but the
// mechanism is not, because the two kernels refuse in opposite places:
//
//   - Unix asks the kernel not to traverse, and the OPEN fails —
//     see nofollow_unix.go.
//   - Windows asks the kernel to open the LINK ITSELF, the open SUCCEEDS, and
//     the handle is then rejected — see nofollow_windows.go.
//
// Closing it on one platform only was the previous answer and it was the wrong
// one: the same deployment would succeed on Linux and fail on Windows for a
// reason the error could not explain (ADR 0081 §Deferred, now closed).
//
// Package lock — the plain open on the platforms that have no file lock at all
// (ADR 0018 §(a)).
//
// js, wasip1, plan9, aix, solaris and illumos reach this file. [NewFileLocker]
// refuses them at CONSTRUCTION through platformNative, so nothing here is
// reachable; it exists because the package must COMPILE on every GOOS, which
// is ADR 0018's build bar, and because the build-tag set is the one the
// kernel's file lock uses for its own refusal (internal/kernel/fs/flock's
// flock_other.go) rather than a fourth shape invented here.
//
// Solaris does have O_NOFOLLOW and is nevertheless served by this file. That
// is deliberate: giving it the Unix open would mean the kernel lock's tag sets
// and nofollow_*.go's disagree, which is how a platform ends up with one half
// of a pair. A platform gains both or neither, and it gains them by acquiring
// a working file lock first — TestTheLockAndItsHardeningShareAPlatform fails
// the day the two sets part.
//
// Package lock — the Unix half of "do not follow an indirection at the lock
// path": O_NOFOLLOW, and a refusal that does not depend on which errno the
// kernel chose to spell it with.
//
// Package lock — the Windows half of "do not follow an indirection at the lock
// path".
//
// # CreateFileW followed reparse points, and that was read rather than assumed
//
// os.OpenFile reaches CreateFileW through syscall.Open, and syscall.Open sets
// FILE_FLAG_OPEN_REPARSE_POINT for exactly ONE createmode: CREATE_NEW, which
// is O_CREAT|O_EXCL. This locker opens O_CREATE|O_RDWR, which is OPEN_ALWAYS,
// which does not get the flag — so before this change the open followed a
// symbolic link or a junction planted at the lock path, the same defect the
// Unix side was measured to have. Read in the toolchain this repository pins
// (go1.27.0, src/syscall/syscall_windows.go), not inferred from behaviour.
//
// # The flag OPENS the link, it does not refuse it — so the check is the pair
//
// FILE_FLAG_OPEN_REPARSE_POINT means "give me a handle to the reparse point
// itself" rather than "fail if there is one". Used alone it fixes the
// redirection — the flock and the ledger stop landing on the attacker's file —
// and leaves the lock sitting on a link the attacker still owns and can
// retarget. So the flag is paired with GetFileInformationByHandle: any handle
// whose attributes carry FILE_ATTRIBUTE_REPARSE_POINT is closed and refused.
// The flag is what makes the check possible (without it the handle is the
// TARGET, which has no reparse attribute and nothing to notice); the check is
// what turns a redirect into a refusal.
//
// # No new dependency, and no hand-rolled CreateFile
//
// golang.org/x/sys is banned SDK-wide, and ADR 0081 bound LockFileEx from
// kernel32 with syscall.NewLazyDLL rather than importing it. Nothing of the
// kind is needed here: go1.27's syscall.Open passes the high 12 bits of its
// flag word through to CreateFileW's dwFlagsAndAttributes, and
// FILE_FLAG_OPEN_REPARSE_POINT is in its validFileFlagsMask — so the flag
// rides the ordinary os.OpenFile call.
//
// That matters beyond tidiness. ADR 0081 §D5 accepts every directory on
// Windows on the strength of one sentence: "os.OpenFile reaches CreateFileW
// WITHOUT FILE_SHARE_DELETE, so a held lock file can be neither deleted nor
// renamed whatever the ACL says". A hand-rolled CreateFile would have had to
// restate that share mode — and a hand-rolled share mode that drifts is how
// the Windows directory rule silently stops being justified. The open stays
// os.OpenFile, so the sentence stays literally true.
package lock
