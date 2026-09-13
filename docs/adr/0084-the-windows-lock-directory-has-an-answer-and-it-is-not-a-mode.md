# ADR 0084 — the Windows lock directory has an answer, and it is not a mode

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0081](0081-the-windows-file-lock-is-a-different-primitive.md) §D5 (Windows accepted every lock directory; it no longer does) and its §Deferred DACL item, [ADR 0083](0083-a-path-is-a-chain-and-a-held-lock-can-lose-its-file.md) §D4 and its §Deferred DACL item — both closed here
- **Related**: [ADR 0052](0052-sdk-lock-domain.md) (the domain), [ADR 0018](0018-sdk-cross-platform-portability.md) (the `x/sys` ban, and the lazy-DLL discipline that answers it), [ADR 0057](0057-sdk-authz-domain.md) (byte-equality comparison of an identifier, rather than a parser)

## Context

Two rules in `internal/service/lock` rest on one question: **is this a
directory any account can put an entry into?**

- `checkDir` asks it of the lock directory (ADR 0052 §D6, ADR 0081 §D5).
- `checkChain` asks it of the directory holding each component of the lock
  directory's path (ADR 0083 §D3).

On Unix it is one mode bit. On Windows `os.Stat` has no permission bits to read
and synthesises a mode from `FILE_ATTRIBUTE_READONLY`, so every writable
directory reports `0777` and every read-only one `0555`. Running the POSIX rule
would refuse **every** directory a caller could name, as a typed
`LOCK_DIRECTORY_UNSAFE` blaming the deployment — ADR 0018 §(a)'s failure mode
exactly. So ADR 0081 §D5 made Windows accept every directory, and ADR 0083 §D4
made `plantable` answer "no" there, each naming the gap rather than hiding it.

The result was an asymmetry with a shape worth stating plainly: **the Unix side
refused a world-writable-without-sticky directory and the Windows side refused
nothing at all.**

Both records deferred the same remedy, in the same words, on the same ground.
ADR 0081 §Alternatives priced a DACL check at roughly 250 lines of ABI and
said it was not iterable locally. ADR 0082 §Deferred carried that forward
untouched. ADR 0083 §Deferred carried it a third time — and, because it now had
two rules depending on it rather than one, wrote down precisely what was
missing instead of restating the price.

**That estimate is what this ADR re-checked, and it is no longer right.**

## Decision

### D1 — The question is asked of the DACL, and the remaining ABI is two calls

`dirWritableByAnyone(dir)` reads the directory's discretionary access control
list and reports whether an access-allowed entry grants a planting right to an
identifier meaning "anybody".

The reason this is now small is that go1.27's `syscall` already exports
everything except the retrieval and the iteration — read in the pinned
toolchain rather than assumed (`src/syscall/security_windows.go`):

| Needed | Where it comes from |
|---|---|
| build a well-known SID | `syscall.StringToSid` (`ConvertStringSidToSidW`) |
| render an ACE's SID back to compare it | `(*syscall.SID).String` (`ConvertSidToStringSidW`) |
| release the security descriptor | `syscall.LocalFree` |
| **retrieve the DACL** | **`GetNamedSecurityInfoW`, bound here** |
| **walk it** | **`GetAce`, bound here** |

Two `advapi32.dll` exports, bound with `syscall.NewLazyDLL` — the discipline
ADR 0081 established binding `LockFileEx` from `kernel32`, and for the same
reason: `golang.org/x/sys` is banned SDK-wide (ADR 0018) and binding two stable
exports adds **no module, no `go.sum` entry and no `MODULE.bazel` change**.

`EqualSid` is not bound at all, and that is where a chunk of the old estimate
went: rendering each ACE's identifier with `(*SID).String` turns the comparison
into a string comparison against a two-element table. It is the same choice
ADR 0057 makes for a resource — byte equality, no parser — and it costs one
call per candidate ACE on a path taken once at construction.

Two structures are hand-declared from `winnt.h`: `ACL` (8 bytes) and the
identical layout `ACCESS_ALLOWED_ACE` and `ACCESS_DENIED_ACE` share. The
object-type ACE variants carry a GUID between the mask and the SID and are
deliberately **not** decoded — an entry whose layout this file does not know is
one it does not judge.

### D2 — "Anybody" is Everyone and Authenticated Users, and not a third

`S-1-1-0` and `S-1-5-11`, which are the two ADR 0081 §D5 named when it
described the check it was deferring. The pair is kept exactly as named.

`S-1-5-32-545` (BUILTIN\Users) is the obvious third candidate and is
**excluded**, with the reason written down rather than the exclusion left
implicit: Windows itself grants Users write on directories it ships, so adding
it would refuse deployments this change has no measurement about. Widening the
set is a decision that needs its own evidence; it is in §Deferred.

### D3 — The MASK is read, not merely the identifier

`plantRights` is `FILE_ADD_FILE | FILE_ADD_SUBDIRECTORY | FILE_DELETE_CHILD |
GENERIC_WRITE | GENERIC_ALL | WRITE_DAC | WRITE_OWNER`.

`FILE_DELETE_CHILD` is in it because unlinking the lock file and creating a new
one is the same attack from the other side — the exposure ADR 0083 §D5 could
only detect. `WRITE_DAC` and `WRITE_OWNER` are in it because an account that
can rewrite the ACL, or take ownership and then rewrite it, can grant itself
the rest: **a right to become writable is a right to write.**

A check that refused any directory naming Everyone at all would pass every
refusing test and quietly refuse a great many safe directories — a
world-READABLE lock directory is not a world-writable one, exactly as `0755` is
not `0777`. `TestADirectoryEveryoneCanOnlyReadIsAccepted` is that row.

Generic rights are EXPANDED before anything is compared. `GENERIC_WRITE` and
`GENERIC_ALL` stand for specific rights through the file object's
`GENERIC_MAPPING`, and Windows normally applies that mapping when an ACE is
stored — but an ACE may still carry them, and comparing the two
representations as unrelated bits gets deny subtraction wrong in BOTH
directions: a specific deny followed by a generic allow leaves the generic bit
standing, and a generic deny never subtracts from a specific allow at all.

Deny entries are subtracted before a later allow is consulted, in list order.
Nothing reorders the list: a DACL that is not in canonical order is evaluated
in the order it is in, and rewriting it here would be a different check.
`INHERIT_ONLY_ACE` entries are skipped — they describe what children inherit
and grant nothing on the directory itself.

### D3b — Denials are accumulated per ACCOUNT, never per identifier

The obvious implementation keys denied rights on each ACE's literal SID and
subtracts them from a later allow carrying the same SID. It is wrong, because
the two identifiers this rule cares about are not independent: **every
authenticated account holds Everyone AND Authenticated Users.** An ACL that
denies Everyone `FILE_ADD_FILE` and then allows Authenticated Users
`FILE_ADD_FILE` grants that right to nobody, and the SID-keyed version reports
it as granted and refuses a directory that is perfectly safe.

So the walk models two hypothetical accounts instead — an unauthenticated one
holding Everyone, and an authenticated one holding both — and a deny reaches
every account holding the identifier it names. Two accounts are enough for two
identifiers, which is the honest cost of this shape and a second reason
widening `anyoneSids` is a decision with its own evidence (D2).

### D3c — The ACE TYPE is checked before the identifier is read

`aceEntry` is the layout of `ACCESS_ALLOWED_ACE` and `ACCESS_DENIED_ACE` only.
An object ACE puts a flags word and up to two GUIDs where that struct puts
`SidStart`, so reading one through it would hand `ConvertSidToStringSidW` bytes
that are not a SID at all. The type check therefore comes first, before
anything reads through the pointer — which is a memory-safety ordering and not
a tidiness one.

### D3d — A directory this process CREATED is checked like any other

`prepareDir` returned as soon as `os.MkdirAll` succeeded, on the reasoning that
a directory we created has the mode we asked for. That holds on Unix and is
false on Windows: a new directory INHERITS its parent's access control list and
`lockDirMode` has no meaning there at all. A lock directory created under an
Everyone-writable parent was therefore accepted on its FIRST construction and
would only have been refused by a later one — the worst shape for a security
check, because the deployment that is wrong is the deployment that never sees
the error.

The create branch now runs the same `checkDir` the existing-directory branch
runs.

### D4 — A NULL DACL is the most permissive state, not the emptiest

`GetNamedSecurityInfoW` reports a NULL DACL for an object with no
discretionary list, and Windows reads that as **everyone, full control**. An
empty DACL — present, zero entries — is the opposite and grants nobody
anything.

Reading NULL as "no entries, therefore no grants" is the exact inversion that
makes a security check worse than none, so it is handled first and reported as
`why=null_dacl`.

### D5 — It fails OPEN, and the asymmetry decides

Every failure on the way to a verdict — a path that cannot be converted to
UTF-16, an object whose security information cannot be read, an ACE that cannot
be fetched — answers "not writable by anybody".

A wrong **refusal** costs a caller a locker that will never build on a
directory that is perfectly safe, on a platform where this code cannot be
iterated locally. A wrong **acceptance** costs the hardening this change adds
and leaves the platform exactly where it was an hour ago. Those are not
symmetric.

The Win32 status travels in the refusal's `mode` field as
`GetNamedSecurityInfoW=<n>`, so an operator can tell "the check ran and
accepted" from "the check could not run" instead of guessing.

### D6 — The tests drive the real access-control model, on a real kernel

`icacls` ships with Windows and needs no privilege to edit an ACL on a
directory the caller owns, so the table applies real grants:

| Row | Grant | Verdict |
|---|---|---|
| ordinary temporary directory | none | accepted |
| `(OI)(CI)W` to `*S-1-1-0` | write | **refused**, `LOCK_DIRECTORY_UNSAFE` |
| `(OI)(CI)R` to `*S-1-1-0` | read | accepted |
| `(OI)(CI)F` to `*S-1-1-0` | full control | **refused**, `LOCK_DIRECTORY_UNSAFE` |
| directory CREATED under an `(OI)(CI)W` parent | inherited write | **refused**, `LOCK_DIRECTORY_UNSAFE` |
| junction in an owner-only container | none | accepted |
| junction in an `(OI)(CI)W` container | write | **refused**, `LOCK_PATH_REDIRECTED` |

The first row is first on purpose. If the runner's temporary directory carried
an Everyone-write entry, this rule would refuse every lock directory the suite
builds and the failure would read as a bug in the lock domain rather than as a
wrong assumption about the runner. So the assumption is asserted rather than
relied on.

The last row is what closes ADR 0083's asymmetry: the Windows chain rule now
refuses a planted junction on the same criterion the Unix one refuses a planted
symbolic link.

**An ACL that cannot be applied is a FAILURE, not a skip**, and that is the one
place this table departs from ADR 0082 §D5's shape. That record's reparse rows
skip when `SeCreateSymbolicLinkPrivilege` is absent, because an unprivileged
account genuinely may not hold it. `icacls` is different: it ships with every
supported Windows and needs no privilege on a directory the caller owns, so a
failure there is a change in the runner image rather than a property of the
account.

The distinction matters because of something ADR 0082 §D5 measured and this
table inherits: `e2e-cross` runs `go test` WITHOUT `-v`, and `go test` buffers
a passing package's output and discards it. A skipped row and a passed row are
the same green tick. Letting the refusing rows skip would make a table that
quietly stopped running indistinguishable from one that was never written —
and the refusing rows are the entire claim of this ADR.

So what a green `windows` cell proves here is stronger than what it proves for
ADR 0082's table: not "at least one row reached a kernel", but "every row did".

The lane is the `windows` job of `.github/workflows/e2e-cross.yml`, which ADR
0082 §D5 already proved executes this package's Windows suite on
`windows-latest`. The Linux Bazel gate compiles neither `dacl_windows.go` nor
its tests, so that lane is the only gate either has.

Measured on this change's run: the `windows` cell reported
`ok github.com/kitsunium/sdk/internal/service/lock` on `windows-latest` with
the fatal-on-`icacls` form in place, so every row of the table applied a real
ACL and got the verdict above.

## Consequences / Semantics

- **A Windows deployment that worked now fails, by design.** A lock directory
  whose DACL grants Everyone or Authenticated Users a planting right is refused
  with `LOCK_DIRECTORY_UNSAFE` — the same code, the same remedy and the same
  exit status the Unix side has had since ADR 0052. `C:\Windows\Temp`-shaped
  directories are the realistic case.
- **The chain rule now refuses on Windows too.** ADR 0083 shipped with
  `plantable` answering "no" there; it now answers the question.
- **The Unix rule is untouched**, mode table included. Windows has no sticky
  bit and therefore no equivalent of the `0777|sticky` exemption — there is no
  Windows ACL that says "anyone may create but only the owner may unlink" — so
  the platforms' accepting sets genuinely differ, and that is a property of the
  two access-control models rather than a divergence introduced here.
- **The dependency graph is untouched**: no module, no `go.sum` entry, no
  `MODULE.bazel` change. Two lazy-bound `advapi32` exports.
- **Cost**: one `GetNamedSecurityInfoW` plus one `GetAce` per candidate entry,
  on `NewFileLocker` only, plus one per component of the path that is an
  indirection. Nothing on `Acquire`, `Extend` or `Release`.

## Breaking changes

**Yes, one, and it is deliberate.** On Windows, `NewFileLocker` now refuses a
lock directory — or a lock directory reached through an indirection — whose
DACL grants a planting right to Everyone or Authenticated Users, where it
previously accepted every directory.

No signature changes, no configuration field, no new symbol, no removal. The
public surface is unchanged: the sentinels involved, `LockDirectoryUnsafe` and
`LockPathRedirected`, already exist.

There is no opt-out. A flag to accept a world-writable lock directory is a flag
to accept a lock any account can replace, and the deployment that would reach
for it is the one that most needs to know.

## Alternatives considered

### Why not `GetEffectiveRightsFromAclW` with a `TRUSTEE`

It answers "what does this identifier effectively hold" in one call and would
remove the ACE walk. It also needs the `TRUSTEE_W` structure hand-declared —
six fields including two pointers and two enums — and it resolves group
membership, which is a *different question*: "is Everyone effectively granted
write" and "is there an entry granting Everyone write" diverge on a directory
whose ACL denies a group Everyone is nested in. The list walk answers the
question the Unix rule asks, and it is the one the ADRs described.

### Why not `AccessCheck`

It evaluates a descriptor against a TOKEN, and the token available here is this
process's. The question is about *another* account — one that does not exist
yet — so there is no token to check against, and impersonating Everyone is not
a thing an unprivileged process can do.

### Why not keep deferring it

Because the ground it was deferred on was a cost estimate, and the estimate was
not re-checked in either of the two records that carried it forward. Two
`advapi32` exports is not 250 lines of ABI. A deferral that survives three ADRs
without anyone re-measuring it has stopped being a decision and become a habit.

### Why not put the SID table in configuration

It is two constants describing the Windows security model, not a policy. A
caller who wants a different set wants a different rule, and ADR 0057's
argument against a policy DSL applies unchanged.

## Deferred

- **`BUILTIN\Users` (`S-1-5-32-545`) as a third "anybody".** It is the
  identifier most Windows deployments actually grant, which is both why it
  would catch more and why adding it blind would refuse directories the
  operating system itself configures. It needs a measurement of what a
  realistic `%ProgramData%`-shaped and `%TEMP%`-shaped directory grants before
  it can be a refusal. See D2.
- **The audit list (SACL).** Not read. It needs `SE_SECURITY_NAME`, a privilege
  an ordinary account does not hold, and it describes what is LOGGED rather
  than what is allowed.
- **Object-type ACEs** (`ACCESS_ALLOWED_OBJECT_ACE` and its denied twin) are
  skipped rather than decoded. They carry a GUID between the mask and the SID,
  they are an Active Directory mechanism, and they do not appear on ordinary
  filesystem objects. An entry whose layout this file does not know is one it
  does not judge — which fails open, consistently with D5.
- **A planted lock file kept held.** Unchanged from ADR 0081 §Deferred: an
  attacker who can write the lock directory can create the lock file before any
  holder and hold it forever. That is a denial of service rather than a lost
  exclusion — and this change now refuses the directory it would happen in,
  which narrows it to directories the rule accepts.

## References

- [ADR 0081 — the Windows file lock is a different primitive](0081-the-windows-file-lock-is-a-different-primitive.md) §D5, §Deferred
- [ADR 0083 — a path is a chain, and a held lock can lose its file](0083-a-path-is-a-chain-and-a-held-lock-can-lose-its-file.md) §D4, §Deferred
- [ADR 0052 — the `lock` domain](0052-sdk-lock-domain.md) §D6
- [ADR 0018 — cross-platform portability](0018-sdk-cross-platform-portability.md)
- `go1.27.0` `src/syscall/security_windows.go` — `StringToSid`, `ConvertSidToStringSidW`, `GetLengthSid`, and the absence of `GetNamedSecurityInfoW`, `GetAce` and `EqualSid`
- `GetNamedSecurityInfoW`: <https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-getnamedsecurityinfow>
- `GetAce`: <https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-getace>
- `ACL` and `ACE_HEADER` layouts: <https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-acl>
- A NULL DACL grants everyone full access: <https://learn.microsoft.com/en-us/windows/win32/secauthz/null-dacls-and-empty-dacls>
- Well-known SIDs: <https://learn.microsoft.com/en-us/windows-server/identity/ad-ds/manage/understand-security-identifiers>
