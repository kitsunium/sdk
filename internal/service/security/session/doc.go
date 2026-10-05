// Package session — the components ABOVE the store directory, which no flag on
// an open can reach.
//
// # What this closes
//
// FileConfig.Dir is a path, and every component of it is a name somebody else
// may have created. Measured against the store as it shipped, on darwin/arm64:
// a symbolic link planted at a parent component inside a world-writable sticky
// directory — the shape /tmp has — was followed by os.MkdirAll, the directory
// check then judged the TARGET, and the store was built and filed every record
// under the planter's tree:
//
//	Dir demandé    = …/pub/app/sessions     (pub is 1777, app -> …/elsewhere)
//	NewFileStore   : err=<nil>
//	New            : err=<nil>
//	record landed under the planted target = true
//
// The owner-only rule on Dir could not see it, because the directory it judged
// was a perfectly private 0700 one: the planter's, or one the store created
// inside the planter's tree.
//
// # The rule is the lock domain's, and so is the reason for it
//
// internal/service/app/lock closed the same gap for its lock directory (ADR 0083),
// on internal/kernel/fs/pathchain, and this is the same rule over the same walk:
// an indirection is refused when the directory HOLDING it is world-writable —
// when anybody could have planted it — and the sticky bit exempts nothing,
// because planting a component CREATES an entry rather than unlinking one.
//
// It is not "refuse a link". /tmp is a symbolic link on macOS, /var/run is one
// on most Linux distributions, and every t.TempDir() on macOS resolves through
// /var -> /private/var. What separates those from an attack is not the link,
// it is the directory the link lives in: one only root can write was set up by
// root.
//
// # What it does not see, and what does
//
// The audit runs once, at construction, BEFORE os.MkdirAll — auditing after it
// would mean refusing the directory only after creating it inside the
// planter's tree. A component replaced AFTER it returns is not seen by it; that
// half is closed by a different mechanism: the store holds its directory as an
// os.Root from construction on and resolves every name against that handle, so
// a parent swapped later moves nothing (see file_store.go).
//
// Package session — constant-time comparison of the values that name a session.
//
// Package session — the in-memory store's construction parameters.
//
// Package session — the at-rest framing of a record.
//
// Package session — the file store's construction parameters.
//
// Package session — one name in the store's held directory: what is there, and
// an open that never reads or locks through an indirection planted at it.
//
// # What this closes
//
// Measured against the store as it shipped, on darwin/arm64:
//
//	symbolic link at <digest>.session -> a copy kept outside the directory
//	Load : err=<nil> where="outside"      (served from the link's target)
//
//	symbolic link at .lock -> a path that does not exist yet
//	NewFileStore : err=<nil>, and the link's target now exists
//
// os.ReadFile and os.OpenFile follow a link at the final component, so the
// first served a session from a file the planter chose and the second created
// a file wherever the link pointed and took the store-wide lock on it. Writes
// were never redirected — rename(2) replaces a link rather than following it —
// which is why this file is about the two OPENS.
//
// # Why not O_NOFOLLOW, which is what internal/service/app/lock uses
//
// Because the store no longer opens anything by path. Every name resolves
// against the os.Root held since construction (file_store.go), which is what
// keeps a parent swapped after construction from moving the store — and
// os.Root has no O_NOFOLLOW: it ORs the flag in itself and then resolves the
// link on the caller's behalf, confined to the root (go1.27,
// src/os/root_unix.go, rootOpenFileNolog). syscall.Openat, which would take
// the flag relative to a held directory, exists in go1.27 for linux, aix and
// wasip1 only (ADR 0083), so it cannot serve darwin or the BSDs.
//
// So the refusal is spelled with what os.Root does offer, in two halves:
//
//   - LOOK FIRST. Lstat through the held directory, which does not follow, and
//     refuse a name that is a link — or anything but a regular file — before
//     anything opens it. A dangling link is never followed into creating its
//     target, and a FIFO is never opened, so it cannot park the reader while
//     it holds the store-wide lock.
//   - THEN PROVE THE HANDLE. A name swapped between the look and the open is
//     followed by os.Root — inside the store directory only, since os.Root
//     refuses a link that leaves it — and is caught by comparing the opened
//     handle with what the name was: os.SameFile on the fstat of the handle.
//     Nothing is read from, or locked on, a handle that fails it.
//
// The two together are what O_NOFOLLOW gives on a path: the open never USES a
// file the name did not lead to. They do not prevent the follow inside the
// store directory during that race, and the only account that can race it
// there is one that can already write a 0700 directory it does not own.
//
// Package session — the file store's reading half, and the lock around it.
//
// Package session — the small filesystem helpers behind atomic publication.
//
// Package session — the on-disk store.
//
// Package session — the file store's writing half.
//
// Package session — the honest refusal on platforms without the mechanics the
// file store requires (ADR 0018 §(a): a uniform typed sentinel where a
// platform has no native mechanic, never a silent drop and never a build
// break).
//
// # Why Windows is refused rather than approximated
//
// The file store's first guarantee is that a session record is unreadable by
// any account but the one that wrote it. On Windows, Go's os.Chmod maps a
// FileMode to the read-only attribute and nothing else: 0600 does not describe
// an ACL, and a file created in a directory with an inheritable permissive DACL
// is readable by whoever that DACL admits.
//
// # The reason this file used to give is no longer the reason
//
// It said the right DACL needs CreateFileW with a security descriptor "which
// stdlib syscall does not expose". That stopped being the obstacle with ADR
// 0081 and ADR 0084: the kernel binds LockFileEx from kernel32
// (internal/kernel/fs/flock) and GetNamedSecurityInfoW and GetAce from
// advapi32 (internal/kernel/fs/winacl) through syscall.NewLazyDLL, with no new
// dependency, and the same mechanism reaches every advapi32 export. What is
// still missing is two things, and neither of them is a reuse of that code:
//
//   - An owner-only DACL, BUILT and then VERIFIED. The kernel's reader
//     (winacl.GrantsAnyone, ADR 0084/0086/0095) builds nothing, and the one
//     question it answers — does an identifier meaning ANYBODY, Everyone,
//     Authenticated Users or BUILTIN\Users, hold a right — is weaker than this
//     store's rule: 0700 and 0600 exclude every other account, a named
//     colleague included. A directory granting read to one named principal
//     passes the reader and fails the Unix rule, so reusing it would ship a
//     weaker guarantee under the same name. Applying a protected owner-only
//     DACL at creation is new ABI (SetNamedSecurityInfoW, or a
//     SECURITY_ATTRIBUTES on the create), with its own tests.
//   - A directory flush. Every rename and unlink here is followed by an fsync
//     of the directory, which is what makes Destroy a revocation a power cut
//     cannot undo. Windows has no equivalent — FlushFileBuffers on a directory
//     handle returns ERROR_ACCESS_DENIED (ADR 0056 D10) — which is why
//     internal/service/data/vfs refuses Windows as well.
//
// The lane that would hold a Windows store to ADR 0018's runtime bar is not
// missing: e2e-cross's Windows job runs this package, where the store's suites
// skip today because the store is refused — a green cross-compile alone would
// not clear that bar.
//
// The lock half is the one piece that exists: the kernel's LockFileEx (ADR
// 0081) could serialise this store's read-modify-write, and it is what
// [fileStore.takeFlock] would call there. It is not enough on its own.
//
// Approximating the rest — calling os.Chmod(0600), observing no error, skipping
// the directory flush, and reporting success — would produce exactly the store
// this domain refuses to be: one that makes a security claim it cannot keep,
// on the platform where nobody would think to check. So the constructor
// returns the typed proc.UnsupportedPlatform and the caller chooses a memory
// store, an external store, or another host. Nothing past that refusal is
// reachable here, the lock calls included.
//
// Package session — the platform gate the file store reads, and the one
// question a mode can answer here and cannot on Windows.
//
// The file store rests on three guarantees, and only two of them are portable.
// Atomic publication is rename(2), which POSIX requires to be atomic and which
// Go's os.Rename also provides on Windows through MoveFileEx. Restrictive
// permissions and advisory locking are not: they are the reason this file has a
// build tag and a sibling that refuses. The lock itself is the kernel's
// (internal/kernel/fs/flock, polled by [fileStore.takeFlock]); what stays here
// is the gate that says both mechanics exist, and [plantable], which a mode
// can answer here and cannot on Windows.
//
// Package session — the in-process store.
//
// Package session — the in-process store's writing half.
//
// Package session — minting a session identifier from a random source.
//
// Package session — the stored form of a session.
//
// Package session — the AEAD sealer that renders an identifier as a cookie
// value.
//
// Package session implements the server-side session domain declared in
// internal/core/security/session (ADR 0045): two concrete stores — one in memory, one on
// disk — and the AEAD sealer that renders a session identifier as a cookie
// value.
//
// Both stores answer the same contract and differ only in where the record
// lives and how long it survives. The memory store dies with the process; the
// file store survives a restart, is confined to one host, and makes real
// operating-system guarantees that this package refuses to fake where the
// mechanism does not exist (see file_store.go and fsguard_unix.go).
//
// Nothing here waits on the wall clock. Every deadline is read from an injected
// kernel/clock.Clock, so an expiry test advances a ManualClock instead of
// sleeping. The narrow half of the port is deliberate: a session store READS
// time, it never waits on it, so it takes Clock rather than Timed.
//
// Package session — the deadline policy shared by every store.
package session
