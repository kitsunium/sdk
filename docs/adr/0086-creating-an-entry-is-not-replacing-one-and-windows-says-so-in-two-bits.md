# ADR 0086 — creating an entry is not replacing one, and Windows says so in two bits

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0084](0084-the-windows-lock-directory-has-an-answer-and-it-is-not-a-mode.md) §D2 (`BUILTIN\Users` is now in the table), §D3 (one mask becomes three), §D3c (two ACE shapes become eight) and §Consequences (the claim that Windows has no `0777|sticky` equivalent). Its §Deferred items 1 and 3 are CLOSED here; items 2 and 4 are re-argued and made PERMANENT.
- **Related**: [ADR 0052](0052-sdk-lock-domain.md) §D6 (the Unix table this one now matches), [ADR 0083](0083-a-path-is-a-chain-and-a-held-lock-can-lose-its-file.md) §D3 (the chain rule, which asks the other question), [ADR 0018](0018-sdk-cross-platform-portability.md) (the `x/sys` ban and the lazy-DLL discipline that answers it)

## Context

ADR 0084 gave Windows a DACL check and left four items deferred. Three of them
rested on a premise; the fourth on a limit. This record measured all four on
`windows-latest`, and three of the premises did not survive.

**What was measured**, by running the shipped walk and by writing access
control lists the shipped walk then read back:

| Object | Entry | Meaning |
|---|---|---|
| `%ProgramData%` | `S-1-5-32-545` mask `0x00000116`, `CONTAINER_INHERIT` | BUILTIN\Users may ADD a file or a subdirectory, here and in every subdirectory, and may NOT unlink one |
| `%ProgramData%` | `S-1-5-32-545` mask `0x001200a9`, `OBJECT_INHERIT|CONTAINER_INHERIT` | …and may only READ the files created there |
| `%SystemRoot%\Temp` | `S-1-5-32-545` mask `0x001f01ff`, no inheritance | BUILTIN\Users holds FULL CONTROL on the directory itself |
| `C:\` | `S-1-5-11` mask `0x00000004`, no inheritance | Authenticated Users may create a subdirectory at the volume root |
| the runner's token | `SeSecurityPrivilege`, present, disabled, and enabled on request | the CI account is the built-in Administrator (RID 500) |
| an NTFS directory | `SetNamedSecurityInfoW` with `ACCESS_ALLOWED_OBJECT_ACE` | refused at ACL revision 2 (`ERROR_INVALID_ACL`, 1336), **accepted and stored at revision 4**, read straight back as type `0x05` |

Three of those lines contradict ADR 0084.

- It says Windows has no ACL meaning "anyone may create but only the owner may
  unlink", so the `0777|sticky` row the Unix table accepts has no equivalent
  here. `%ProgramData%` is that ACL, and Windows ships it.
- It says `C:\Windows\Temp`-shaped directories are "the realistic case" of the
  deployments its breaking change refuses. They were not refused: the entry
  that makes that directory dangerous names BUILTIN\Users, and BUILTIN\Users
  was excluded from the table.
- It says object-type ACEs "do not appear on ordinary filesystem objects", so
  skipping them is safe. An ordinary NTFS directory stores one, and the check
  walked straight past a grant of `FILE_DELETE_CHILD` to Everyone.

## Decision

### D1 — Two rules, three masks, and none of them is "can anybody write here"

`checkDir` and `plantable` ask different questions. The Unix side has always
known that — `checkDir` accepts `0777|sticky` and `plantable` refuses it,
because sticky governs UNLINKING an entry that exists while planting a
component CREATES one at a free name (ADR 0083 §D3). ADR 0084 gave the two
rules one `plantRights` mask on Windows, which is the thing the POSIX pair
deliberately does not do.

So the mask splits, and a third appears that Unix has no need of:

| Mask | Rights | Asked by | Because |
|---|---|---|---|
| `replaceRights` | `FILE_DELETE_CHILD`, `WRITE_DAC`, `WRITE_OWNER` | `checkDir`, of the lock directory | these take away the entry a holder created — the exposure is the INODE |
| `createRights` | `FILE_ADD_SUBDIRECTORY`, `WRITE_DAC`, `WRITE_OWNER` | `plantable`, of the directory an indirection was found in | a path component is a directory, so this is what plants one |
| `contentRights` | `FILE_WRITE_DATA`, `FILE_APPEND_DATA`, `DELETE`, `WRITE_DAC`, `WRITE_OWNER`, read off entries carrying `OBJECT_INHERIT_ACE` | `checkDir`, of the FILES the directory will create | see D3 |

`WRITE_DAC` and `WRITE_OWNER` are in all three, unchanged from ADR 0084 §D3: a
right to become writable is a right to write.

`FILE_ADD_FILE` leaves `checkDir` and that is the change with the largest
blast radius. It is in `contentRights` under its other name and in neither of
the directory masks, because creating an entry at a free name is exactly what
the sticky bit permits and what ADR 0052's table has accepted since it was
written.

### D2 — Windows DOES have the `0777|sticky` shape, and spells it in two bits

ADR 0084 §Consequences: "there is no Windows ACL that says 'anyone may create
but only the owner may unlink' — so the platforms' accepting sets genuinely
differ".

`%ProgramData%` grants `BUILTIN\Users` `FILE_ADD_FILE | FILE_ADD_SUBDIRECTORY`
and not `FILE_DELETE_CHILD`, on the directory and on every subdirectory it will
ever have. That sentence is the definition of a sticky directory, and Windows
configures its own machine-wide state directory that way.

The accepting sets do not differ; Windows just spells with two bits what POSIX
spells with one, which makes its answer FINER rather than absent. That is what
makes D4 possible: it is the reason including a third identifier stops being a
breaking change.

### D3 — The lock FILE inherits the directory's list, and no Unix rule has to care

On Unix a lock file is created `0600` whatever the directory's mode says, so a
world-writable directory never makes the lock file world-writable. On Windows
the mode handed to `os.OpenFile` is meaningless and the new file takes the
directory's inheritable entries instead.

So a directory nobody can unlink from can still hand every account the fencing
ledger, and ADR 0084 could not see it twice over: it never asked what the files
would inherit, and it SKIPPED `INHERIT_ONLY_ACE` entries outright — which is
precisely where a grant that reaches only the files is written.

`checkDir` therefore reads a second mask off the entries carrying
`OBJECT_INHERIT_ACE`, whether or not they also apply to the directory. The
ledger is protected by `LockFileEx`'s mandatory range only while somebody
holds it (ADR 0081); between holds it is an ordinary file, and a stranger who
can write it can roll the fencing counter backwards.

### D4 — `BUILTIN\Users` is the third "anybody" — ADR 0084 §Deferred item 1, CLOSED

`S-1-5-32-545` joins `S-1-1-0` and `S-1-5-11` in `anyoneSids`.

ADR 0084 §D2 had the argument right and the consequence wrong. The argument:
every local interactive account is in that group, and on a domain-joined
machine so is Domain Users, so a directory granting it a right IS one the
rule's own sentence describes. The consequence it feared: including it refuses
`%ProgramData%`-shaped lock directories, which is "a breaking change of a
different size".

That consequence was true only while both rules shared one mask. With D1 in
place the measurement says otherwise, and both halves are asserted on the real
directory rather than on one the test built:

- a lock directory under `%ProgramData%` is **accepted**, because the grant
  there is create-and-not-replace and the files inherit read-only;
- a junction planted beside it is **refused**, because `createRights` is
  exactly what that same entry does grant.

The two rules disagree about one identifier on one directory, which is the
whole of D1 shown on a deployment nobody configured for the test.

ADR 0084 §D3b said a third identifier would need a third hypothetical account.
It does, and it has one: the anonymous caller holding Everyone, an
authenticated one holding Authenticated Users as well, and a local interactive
one holding all three. The middle account is kept even though Windows ships
Authenticated Users as a MEMBER of BUILTIN\Users, because that membership is a
default an administrator can edit.

### D5 — The audit list stays unread, permanently, and the privilege was never the reason — ADR 0084 §Deferred item 2, PERMANENT

ADR 0084 deferred the SACL because "it needs `SE_SECURITY_NAME`, a privilege an
ordinary account does not hold". Measured: `windows-latest` holds it — the CI
account is the built-in Administrator — it is disabled by default and
`AdjustTokenPrivileges` enables it on request, and `GetNamedSecurityInfoW`
returns `ERROR_SUCCESS` for `SACL_SECURITY_INFORMATION` on a directory this
process owns. So the stated obstacle is absent on the one machine that could
have tested it.

It stays unread anyway, and now for a reason that cannot expire: **no ACE type
a SACL may carry grants anything.**

| SACL entry | What it does |
|---|---|
| `SYSTEM_AUDIT_ACE`, `SYSTEM_ALARM_ACE` | describe what is LOGGED |
| `SYSTEM_MANDATORY_LABEL_ACE` | the integrity level, which only REFUSES a writer below it |
| `SYSTEM_SCOPED_POLICY_ID_ACE` | a central access policy, which intersects with the DACL and never widens it |
| `SYSTEM_RESOURCE_ATTRIBUTE_ACE` | metadata a condition may read |
| `SYSTEM_ACCESS_FILTER_ACE` | a further restriction |

Reading it could only move this verdict towards ACCEPTING — a mandatory label
can prove that a DACL grant is unreachable for some callers — and accepting is
the direction the rule already fails in (ADR 0084 §D5). A check cannot be made
stronger by reading a list that can only subtract.

And the privilege is a second, independent reason not to: the runner holds it
because it is an Administrator. A rule that behaved differently for a
privileged account than for an unprivileged one would be tested in CI under
conditions no deployment has.

### D6 — Every discretionary ACE shape is decoded; none is skipped — ADR 0084 §Deferred item 3, CLOSED

A DACL may carry eight entry types. ADR 0084 decoded two and stepped over the
rest, on the rule that "an entry whose layout this file does not know is one it
does not judge" — which fails open, and which is only defensible while the
layouts are genuinely unknown. They are in `winnt.h`, and they reduce to two
shapes and one policy:

| Types | Shape | Identifier at |
|---|---|---|
| `ACCESS_{ALLOWED,DENIED}_ACE` | plain | offset 8 |
| `ACCESS_{ALLOWED,DENIED}_CALLBACK_ACE` | plain, with an expression AFTER the identifier | offset 8 |
| `ACCESS_{ALLOWED,DENIED}_OBJECT_ACE` | flags word, then 0–2 GUIDs | 12 + 16 × announced GUIDs |
| `ACCESS_{ALLOWED,DENIED}_CALLBACK_OBJECT_ACE` | both at once | 12 + 16 × announced GUIDs |

The GUID count comes from the entry's own flags word, so the offset is data and
the size is checked against `AceSize` before the pointer is formed — an entry
with no room for even the shortest legal SID is not read at all. That bound is
the memory-safety half of ADR 0084 §D3c's ordering, which is otherwise
unchanged: the TYPE is resolved before anything reads through the pointer.

The premise this closes on is measured rather than argued. ADR 0084 said object
ACEs "do not appear on ordinary filesystem objects"; `SetNamedSecurityInfoW`
stores one on an NTFS directory at ACL revision 4 — revision 2 is refused with
`ERROR_INVALID_ACL` — and `GetNamedSecurityInfoW` reads it back unchanged. A
directory granting Everyone `FILE_DELETE_CHILD` through one was accepted.

The callback shapes need the one policy: they carry a conditional expression
this package does not evaluate, and the two dispositions are resolved in the
single direction that cannot weaken the verdict — **a conditional allow is read
as granting, a conditional deny as denying nothing.** Skipping both instead
would make "add a condition" a way to put a grant where this rule cannot see
it.

### D7 — A planted lock file kept held is what the accepting half BUYS — ADR 0084 §Deferred item 4, PERMANENT

An account that can create an entry in the lock directory can create the lock
file before any holder and keep it held. That is unchanged from ADR 0081
§Deferred, and it is not going to close, because it is not a defect in the
check — it is the price of the row the check deliberately accepts.

Every directory a shared lock can live in is one some other account can write:
that is what "shared" means. A lock has no way to tell a squatter from a peer,
because a peer holding the lock and an attacker holding the lock are the same
bytes and the same kernel state. Refusing them would mean refusing every
directory more than one account can reach, which is refusing the domain's own
use case.

What D1 does change is its reach, and in both directions honestly stated: it is
now the exposure of every directory `checkDir` accepts on the create-not-
replace ground, which is more directories than ADR 0084 accepted — and it is
the exposure `/tmp` has carried on Unix since ADR 0052 §D6, unchanged and
un-noticed, which is the argument for accepting it rather than an excuse.

It is a denial of service. It does not merge two holders, it does not split
one, and it does not move the fencing counter: ADR 0083 §D5's
`LOCK_FILE_REPLACED` detection still reports a lock file that goes away
underneath a holder.

## Consequences / Semantics

- **A Windows deployment that worked now fails, and one that failed now
  works.** Both are deliberate and both are in D1's table.
- **`%SystemRoot%\Temp` is refused**, which is what ADR 0084 §Consequences
  announced and did not deliver. Every local interactive account holds full
  control there, `FILE_DELETE_CHILD` included.
- **`%ProgramData%` is accepted**, which is what makes D4 possible at all.
- **A directory whose only broad grant is "create an entry" is now accepted**
  where ADR 0084 refused it. `C:\` is one such directory on the measured
  image. This is the relaxation, it is named rather than buried, and it is the
  Unix `0777|sticky` row.
- **A directory whose FILES inherit a broad write is now refused** where ADR
  0084 accepted it, including when the entry is `INHERIT_ONLY` and grants
  nothing on the directory itself.
- **The Unix rule is untouched**, mode table included.
- **The dependency graph is untouched**: no module, no `go.sum` entry, no
  `MODULE.bazel` change. The same two lazy-bound `advapi32` exports; a third,
  `SetNamedSecurityInfoW`, is bound in a TEST and nowhere else, because the
  check reads a list and never writes one.
- **Cost** is unchanged: one `GetNamedSecurityInfoW` plus one `GetAce` per
  entry, on `NewFileLocker` only.

## Breaking changes

**Yes, two, in opposite directions.**

1. On Windows, `NewFileLocker` now refuses a lock directory that grants
   Everyone, Authenticated Users or **BUILTIN\Users** the right to unlink an
   entry it does not own, or that will give one of them write access to the
   lock files created in it. `%SystemRoot%\Temp` is the realistic case, this
   time measured.
2. On Windows, `NewFileLocker` now ACCEPTS a lock directory whose only broad
   grant is the right to create an entry at a free name. `%ProgramData%` and
   `C:\` are the realistic cases.

No signature changes, no configuration field, no new symbol, no removal, no new
error code. `LockDirectoryUnsafe` and `LockPathRedirected` already exist.

There is still no opt-out, for ADR 0084's reason unchanged.

## Alternatives considered

### Why not keep one mask and simply leave `BUILTIN\Users` out

That is ADR 0084's position, and it is the one the measurement refutes. Leaving
the group out means a lock directory under `%SystemRoot%\Temp` — where every
local account holds full control — is accepted, which is the exact deployment
ADR 0084 §Consequences told readers it had started refusing. A record that
announces a breaking change and does not make it is worse than one that makes
no claim.

### Why not include `BUILTIN\Users` and keep ADR 0084's single mask

Then `%ProgramData%` is refused, and with it every machine-wide Windows
deployment, on the ground that a group can create an entry there — a right the
Unix table has accepted since ADR 0052 under the name "sticky". It would be a
large breaking change bought with no security gain the split does not already
provide.

### Why not evaluate the conditional expression in a callback ACE

It is a binary-serialised expression language (`artx`) over token attributes,
resolved against a caller's token at access-check time. There is no token here
— the question is about an account that does not exist yet, which is also why
ADR 0084 declined `AccessCheck` — so evaluating it is not merely expensive, it
is undefined. D6's two-line policy is what an unevaluable expression reduces
to.

### Why not read `FILE_ADD_FILE` as a replace right after all

Because an account that creates the lock file first OWNS it, and what it can
then do to its own file is D7's denial of service rather than a substitution of
somebody else's. The line between the two is `FILE_DELETE_CHILD`: taking away
an entry a holder created is what makes two processes lock two inodes and both
believe they hold one lock.

### Why not model one hypothetical account per identifier instead of three nested ones

Because the identifiers are nested in the token, not independent: a deny
addressed to Everyone constrains a later allow addressed to Authenticated
Users. ADR 0084 §D3b established that with two, and adding a third identifier
is what its own sentence said would cost a third account.

## Deferred

- **A planted lock file kept held.** See D7 — deliberately PERMANENT. It is
  the price of the accepting half of `checkDir` on both kernels, not a gap in
  it, and a lock cannot distinguish a squatter from a peer.
- **The audit list (SACL).** See D5 — deliberately PERMANENT. No entry it can
  carry grants anything, so reading it could only move the verdict towards
  accepting.
- **A component replaced AFTER `checkChain` returns.** Unchanged from ADR 0083
  §Deferred. The audit is taken once, at construction.
- **`plantable` reads other-write and not other-execute on Unix.** Unchanged
  from ADR 0083 §Deferred; the Windows half has no equivalent ambiguity,
  because traversal and creation are separate bits there.
- **An account that can remove the lock DIRECTORY and put back one it owns.**
  Found while drawing D1's masks and deliberately left open, because closing
  half of it would be worse than naming all of it. Two rights reach it: `DELETE`
  on the lock directory itself, which `checkDir` reads and does not count, and
  `FILE_DELETE_CHILD` on the directory's PARENT, which nothing reads at all —
  `checkChain` consults a parent's list only where a component is an
  indirection. Either one removes an EMPTY lock directory and recreates one
  whose list the remover chooses. Counting only the first would refuse a
  deployment while leaving the identical exposure one level up, so the whole of
  it is a third rule with its own accepting set and its own record.
- **A directory whose ACL is rewritten between the check and the acquisition.**
  Both rules run at construction and neither re-reads. Closing it needs a
  descriptor pinned at open time and a security query against a HANDLE rather
  than a name, which is a different ABI and a different record.
- **`GENERIC_MAPPING` is the file object's, hard-coded.** `GENERIC_WRITE` and
  `GENERIC_ALL` are expanded through `FILE_GENERIC_WRITE` and
  `FILE_ALL_ACCESS` rather than through the mapping the object's own type
  reports. It is right for every file and directory, and this rule is asked
  about nothing else.

## References

- [ADR 0084 — the Windows lock directory has an answer, and it is not a mode](0084-the-windows-lock-directory-has-an-answer-and-it-is-not-a-mode.md) §D2, §D3, §D3b, §D3c, §Consequences, §Deferred
- [ADR 0083 — a path is a chain, and a held lock can lose its file](0083-a-path-is-a-chain-and-a-held-lock-can-lose-its-file.md) §D3, §D5
- [ADR 0081 — the Windows file lock is a different primitive](0081-the-windows-file-lock-is-a-different-primitive.md) §D5, §Deferred
- [ADR 0052 — the `lock` domain](0052-sdk-lock-domain.md) §D6
- [ADR 0018 — cross-platform portability](0018-sdk-cross-platform-portability.md)
- `ACE_HEADER` and the discretionary ACE layouts: <https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-ace_header>
- `ACCESS_ALLOWED_OBJECT_ACE`: <https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-access_allowed_object_ace>
- `ACCESS_ALLOWED_CALLBACK_ACE` and conditional expressions: <https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-dtyp/c9579cf4-0f4a-44f1-9444-422dfb10557a>
- SACL entry types and the mandatory integrity label: <https://learn.microsoft.com/en-us/windows/win32/secauthz/access-control-lists>
- Central access policies intersect with the DACL: <https://learn.microsoft.com/en-us/windows-server/identity/solution-guides/dynamic-access-control-overview>
- `SE_SECURITY_NAME` and `ACCESS_SYSTEM_SECURITY`: <https://learn.microsoft.com/en-us/windows/win32/secauthz/sacl-access-right>
- File and directory access rights: <https://learn.microsoft.com/en-us/windows/win32/fileio/file-security-and-access-rights>
