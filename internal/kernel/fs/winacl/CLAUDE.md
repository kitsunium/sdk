<!-- updated: 2026-10-03T12:00:00Z -->
# internal/kernel/fs/winacl/

## Purpose

The SDK's Windows DACL reader. Stdlib-only, domain-neutral.
`GrantsAnyone(dir, onDirectory, onFilesWithin)` reads a directory's
discretionary access control list and reports whether an identifier meaning
ANYBODY — Everyone (`S-1-1-0`), Authenticated Users (`S-1-5-11`),
BUILTIN\Users (`S-1-5-32-545`) — holds a right in `onDirectory` on the
directory itself, or a right in `onFilesWithin` through the entries the files
created in it inherit. It is the Windows answer to the question a Unix rule
asks of a mode, and like the rest of this family it is a measurement, never a
verdict.

It was written by `internal/service/app/lock` for the lock directory's rules
(ADR 0084, completed by ADR 0086) and then exported from there so that
`internal/service/data/queue` could ask the same question of its own
directories (ADR 0095) — a service-to-service edge, Windows only, for one
function. ADR 0159 put it where both reach it without reaching into each other:
the reader moved here unchanged, with the tests that measure the walk itself,
and each caller kept its masks and its own answer to "could not look".

## Contents

| File | Holds |
|---|---|
| `winacl.go` | the package doc; the rights `RightAddFile`, `RightAddSubdirectory`, `RightDeleteChild`, `RightDelete`, `RightWriteDAC`, `RightWriteOwner`, and the three masks built from them, `ReplaceRights`, `CreateRights`, `ContentRights`. Every platform compiles it, so a rule can name its masks in a file without a build tag |
| `dacl_windows.go` | `GrantsAnyone`: `GetNamedSecurityInfoW` + `GetAce` from `advapi32`, the eight discretionary ACE shapes, the generic-right expansion, `reachOf` (which object an entry governs), the SID bounds — and the 250-line estimate that deferred it three times, re-checked |
| `tokens_windows.go` | `tokenSet`: the three nested accounts a DACL is evaluated for — a deny reaches every account holding the SID it names, which a map keyed by the ACE's SID gets wrong (ADR 0084 §D3b; the third account, ADR 0086) |
| `walk_internal_windows_test.go` | the walk over lists assembled byte by byte and read with the SHIPPED `GetAce`: every ACE shape, deny-then-allow across nested identifiers, conditional entries, inheritance, separate denial states, and entries too small for the SID they claim |
| `noverdict_external_windows_test.go` | every answer without a verdict is named, against a list read to the end that names nothing |

## What it answers, and what it leaves to its caller

| `granted` | `observed` | Meaning |
|---|---|---|
| `true` | `S-1-1-0=0x40` (identifier, the asked bits it holds), or `null_dacl` | a grant was FOUND |
| `false` | empty | the list was read to the end, and nobody meaning anybody holds a right asked about |
| `false` | `GetNamedSecurityInfoW=5`, `UTF16PtrFromString=…`, `GetAce#3` | NO verdict: the list could not be read, or only in part |

The third row is not the second, and the reader never chooses between them —
its two callers choose opposite ways, each for a reason the other does not
have:

| Caller | Asks | On "no verdict" | Why |
|---|---|---|---|
| `internal/service/app/lock` | `ReplaceRights` of the lock directory and `ContentRights` of what its lock files inherit (`checkDir`); `CreateRights` of the directory holding a path component (`plantable`) | ACCEPTS, and logs | a wrong refusal costs a locker that never builds on a safe directory; a wrong acceptance leaves the platform where it already was (ADR 0084 §D5) |
| `internal/service/data/queue` | `ReplaceRights` of the queue directory; `RightAddFile`, `RightAddSubdirectory`, `ReplaceRights` and inherited `ContentRights` of each state directory | REFUSES, `why=unverifiable` | a queue directory wrongly accepted is a planted message a consumer acts on, and the queue refused every Windows directory before this rule, so the refusal takes away nothing that worked (ADR 0095) |

## Rules from ADR 0084, ADR 0086 and ADR 0095 — the reader's half

Superseded by ADR 0154 (the charter); the ADRs stay as the incidents' record.
Their rules about the READER live here; their rules about the lock directory's
POLICY — which masks it asks, and that it fails open — live in
`internal/service/app/lock/CLAUDE.md`.

- **The MASK is read, not just the identifier.** A world-READABLE directory is
  not a world-writable one, exactly as `0755` is not `0777`.
- **Generic rights are expanded before anything is compared.** A specific deny
  and a generic allow — or the reverse — compared as unrelated bits get the
  subtraction wrong in both directions.
- **Denials accumulate per modelled ACCOUNT, in list order.** Every
  authenticated account holds Everyone AND Authenticated Users, and every local
  interactive one BUILTIN\Users on top, so a deny to Everyone cancels a later
  allow to Authenticated Users; keying denials by the ACE's SID reports a grant
  nobody holds.
- **A NULL DACL is the most permissive list there is** — everyone, full
  control — and is reported as a grant (`null_dacl`); an EMPTY DACL grants
  nothing.
- **Every discretionary ACE shape is decoded** — the plain pair, the object
  pair with 0-2 GUIDs between mask and SID, the two callback pairs — and the
  type is checked BEFORE anything is read through the entry, whose SIZE bounds
  both where the SID starts and how long it says it is (ADR 0086 §D6). An NTFS
  directory does store an object-type entry: measured, against ADR 0084's
  premise.
- **A conditional entry is read in the direction that cannot hide a grant**:
  a conditional allow grants, a conditional deny takes nothing away.
- **The directory and its files keep separate denial states.** An entry
  reaches the directory unless `INHERIT_ONLY`, and the files created in it when
  `OBJECT_INHERIT`; a deny that reached one object must not cancel an allow on
  the other (ADR 0086 §D3b).
- **The SACL is never read.** Nothing a system list carries GRANTS anything —
  audit and alarm entries log, the mandatory label and the access filter only
  restrict — so reading it could only move a verdict towards "nobody may"
  (ADR 0086 §D5).
- **Every no-verdict answer is named** (ADR 0095): before it, two of the three
  came back empty, indistinguishable from a verdict, and the queue could not
  refuse where the lock accepts.
- *Lesson*: one mask for two questions accepted a lock directory whose new lock
  files every account could rewrite; a walk that skipped six of the eight ACE
  shapes failed open through every one of them.

## Conventions

- **It measures and never decides.** No refusal, no log line, no fail-open or
  fail-closed policy: `granted` and `observed`, and the caller's rule.
- **No error codes.** The package emits none and owns no dotted-quad range; a
  failed Win32 call travels as its status inside `observed`.
- **No dependency.** Two `advapi32` exports through `syscall.NewLazyDLL` — no
  `golang.org/x/sys` (ADR 0018) — and the standard library's `StringToSid`,
  `(*SID).String` and `LocalFree`; SIDs are compared as rendered strings, so
  `EqualSid` is never bound.
- **The vocabulary is portable, the reader is not.** `winacl.go` compiles
  everywhere; `GrantsAnyone` exists on Windows only, so a Unix caller cannot
  ask a DACL question by accident and must ask its mode.

## Do NOT

- **Fork the reader for another package.** A new question is a new mask, built
  from the exported rights, not a new walk — a second reader is a second place
  to get eight ACE shapes, the deny subtraction and the NULL DACL wrong.
- **Put a policy here** — a fail-open default, a refusal, a log line. The two
  callers want opposite answers to the same failure.
- **Return a non-empty `observed` with `granted` false when the list WAS read
  to the end.** An empty `observed` is the only way a caller knows a verdict
  was reached; the lock's `checkChain` reads exactly that.
- **Read a NULL DACL as "no entries, so no grants".** That inversion makes a
  security check worse than none.
- **Accumulate both questions against one denial state**, or **key denials
  by SID** — see the rules above.
- **Skip an ACE type because its layout is unfamiliar.** The layouts are in
  `winnt.h`; what is checked before the pointer is formed is the entry's SIZE.
- **Give `GrantsAnyone` a non-Windows stub.** On Unix the question is a mode
  and the caller asks that; a stub answering "no verdict" everywhere would
  invite the mode rule's absence.

## Verification

```sh
GOWORK=off GOOS=windows go -C internal/kernel vet ./fs/winacl/
GOWORK=off GOOS=windows go -C internal/kernel test -c -o /dev/null ./fs/winacl/
bazel build //internal/kernel/fs/winacl   # the portable half; the reader is Windows-only
```

Every test file is Windows-only, so this package has no test to run on Linux,
macOS or the BSDs — that is not a rule-12 exclusion of the kind that hides a
test: there is no configuration in which the reader is built and its tests are
not. The lane that runs them is the `windows` job of
`.github/workflows/e2e-cross.yml` (`./fs/winacl` in `KERNEL_PKGS`, and the
whole-suite step). The real-ACL rows — `icacls` grants, `mklink /J` junctions,
an object ACE written with `SetNamedSecurityInfoW`, `%ProgramData%` and
`%SystemRoot%\Temp` — are each caller's, driven through its own rule:
`internal/service/app/lock`'s `dacl_windows_test.go`,
`dacl_objectace_windows_test.go` and `chain_windows_test.go`, and
`internal/service/data/queue`'s `file_layout_windows_test.go`.
