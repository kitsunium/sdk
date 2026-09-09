// Package lock — range 0.2.21.* (ADR 0052 core/lock block).
package lock

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.21.0 - 0.2.21.255

// CodeLockMisconfigured identifies a locker refused AT CONSTRUCTION because
// its configuration cannot be honoured — most importantly a lease TTL that is
// not positive.
//
// A zero TTL is refused rather than read as "expires immediately" or as "never
// expires". Both readings are defensible and they are opposites, so whichever
// the SDK picked would be wrong for half its callers, and wrong SILENTLY: a
// lock granted with an already-elapsed lease reports success on every call and
// excludes nobody. This is ADR 0031's refuse half at its sharpest.
const CodeLockMisconfigured errs.Code = 0x00_02_15_01 // 0.2.21.1

// CodeLockNotHeld identifies an Extend or a Release from a holder that no
// longer owns the lock — its lease expired and someone else took it.
//
// It is never a cosmetic outcome. On Extend it means the caller is inside a
// section another holder also believes it owns. On Release it means the call
// released nothing, deliberately: releasing under a name that now belongs to
// someone else would unlock THEIR critical section.
const CodeLockNotHeld errs.Code = 0x00_02_15_02 // 0.2.21.2

// CodeLockBackendFailed identifies a locker that could not answer at all — the
// medium, the filesystem, the process it depends on.
//
// A lock simply being HELD is not this: TryAcquire reports that as
// (nil, false, nil), because a caller that offered to give up immediately has
// had its offer accepted and nothing went wrong.
const CodeLockBackendFailed errs.Code = 0x00_02_15_03 // 0.2.21.3

// CodeLockNameRejected identifies a lock name a locker refuses: the empty
// string, or one longer than the backend can represent.
//
// An empty name is refused rather than accepted as a legitimate key, because
// it is what an uninitialised variable produces: accepting it would silently
// serialise every unrelated caller in the program through one lock, which
// looks like a mysterious performance collapse and never like a bug.
const CodeLockNameRejected errs.Code = 0x00_02_15_04 // 0.2.21.4
