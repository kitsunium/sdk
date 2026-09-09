# ADR 0045 — server-side session domain (`session`)

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0042](0042-sdk-token-domain.md) (the token domain — the thing a session is constantly confused with), [ADR 0013](0013-sdk-crypto-domain.md) (the AEAD the sealer and the file store are built on), [ADR 0018](0018-sdk-cross-platform-portability.md) (build bar / runtime bar, and the `UnsupportedPlatform` sentinel), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is a safe default or an explicit refusal), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows a sibling, never a method), [ADR 0035](0035-pp-range-ownership-enforcement.md) (range ownership), [ADR 0001](0001-sdk-go-multimodule-layout.md) (the 4-layer shape)
- **Amends**: `internal/core/CLAUDE.md` §Purpose — a 14th core sibling

## Context

Every application that authenticates a human needs somewhere to keep "who this
browser is" between requests. The SDK already ships the *stateless* half of that
problem — `token` (ADR 0042) mints and verifies JWT and PASETO — and stops
there. The stateful half was missing, and the gap is not cosmetic: the two
mechanisms differ on exactly the property most people assume they share.

| | Token (ADR 0042) | Session (this ADR) |
|---|---|---|
| Where the facts live | inside the value, signed | on the server |
| Verification needs | a key | a key **and a lookup** |
| Revocation | **impossible** — only expiry ends it | immediate, on the next request |
| Scales across processes sharing nothing | yes | only as far as the store does |
| What the value reveals | its claims, to anyone who base64-decodes it | nothing |

"Log out everywhere" means *wait* for a token and *now* for a session. A team
that reaches for a JWT because it is the fashionable answer, and then discovers
they cannot revoke it, has made an architectural decision by accident. Shipping
only the token half made the SDK complicit in that.

Sessions are also a domain where the *common* implementation is subtly wrong in
four well-catalogued ways, and where each of the four is invisible until it is
exploited:

1. **Session fixation.** An attacker plants an identifier, the victim
   authenticates, and the identifier keeps working — so the attacker is now
   logged in as the victim. The fix (rotate at the privilege boundary) is
   universally known and universally forgotten, because in every framework it is
   a *separate step*.
2. **The identifier treated as data.** It is a bearer secret. It gets `%v`'d
   into a log line, compared with `==`, used as a map key, written to disk as a
   filename, and shipped to an aggregator that was never scoped to hold it.
3. **One expiry instead of two.** A sliding session with no absolute ceiling
   never dies. An absolute-only session logs an active user out mid-form. Almost
   every implementation ships one of the two and calls it a policy.
4. **A file store that pretends.** `os.Chmod(0600)` returns `nil` on Windows and
   changes nothing meaningful; a `rename` onto unflushed data is atomic about
   nothing after a power cut; two processes sharing a directory lose updates.

And there is a fifth risk that is not a bug but a trajectory: a session package
that starts by storing sessions ends up owning login routes, access voters, a
user model and a middleware stack. That is a framework, and it is not what a
normed toolbox is for.

## Decision

### D1 — `internal/core/session` as the 14th core sibling, with NO registry

`Store`, `Sweeper`, `Sealer`, `ID`, `SessionValue`, `StateValue` and nine
sentinels in `internal/core/session` (`0.2.14.*`); two stores and the sealer in
`internal/service/session` (`0.3.46.*`); aliases in `pkg/v1/session`.

**No registry**, joining `proc` (ADR 0016), `resilience` (ADR 0026), `net`
(ADR 0029), `scheduler` (ADR 0041) and `token` (ADR 0042). A registry earns its
place when an open set of interchangeable schemes is selected *by data*. Here
the set is closed and the choice — ephemeral or persistent — is a deployment
decision made in code. Resolving it from a configuration string would let a typo
silently downgrade a persistent store to one that dies with the process.

**`Store` is frozen at five methods** (`New`, `Load`, `Save`, `Regenerate`,
`Destroy`). `pkg/v1/session` aliases it, so the shape is published and Go
interfaces are structural: a sixth method breaks every downstream implementer at
compile time with no deprecation window (ADR 0039). Capabilities beyond the port
are **sibling interfaces reached by type assertion**, the shape codec already
uses for `Appender` (ADR 0037). Two exist in this change: `Sweeper`, and
`io.Closer` on the file store — which needed no declaration at all, because the
stdlib already had the right interface.

### D2 — session fixation is prevented by ABSENCE, not by a step

**`Store.Regenerate(ctx, current, subject)` is the only call that binds a
subject, and it mints a new identifier every time.**

There is no `SetSubject`, no `Store.Elevate`, and no `SessionValue.WithSubject`.
`Store.Save` writes the *data* and never the subject: a value whose subject
differs from the stored record is refused with `FixationRefused`. "Keep this
identifier and log this user in" is a sentence with no method in this API.

That is the whole mechanism, and it is deliberately a shape rather than a rule.
A rule ("remember to rotate on login") is what every framework already has, and
it is what every framework already gets wrong. The guards are executable, not
documentary:

- `TestRegenerateIsTheOnlyWayToNameASubject` reflects over the `Store` interface
  and fails if any method other than `Regenerate` accepts a `string`. A
  contributor adding the convenient-looking `SetSubject(ctx, id, subject)` fails
  it before review.
- `TestSessionValueHasNoSubjectMutator` reflects over the value type's method
  set and fails on anything named `*Subject*` other than the reader.
- `TestSaveRefusesToRebindASubject` builds the forged session — which *is*
  constructible, because `StateValue` is exported so third parties can implement
  the port — and asserts it is refused, loudly, rather than written or silently
  reduced to a data-only write.

The forgery being *constructible and not persistable* is the point. It keeps
`FIXATION_REFUSED` a runtime verdict a test can reach instead of a comment
asking people to behave.

### D3 — the identifier is a secret, and the TYPE behaves like one

`ID` is 32 bytes (256 bits) from `crypto/rand`, minted through `io.ReadFull` so
a short read is a failure rather than a weaker identifier.

| Property | Mechanism |
|---|---|
| Cannot reach a log line by accident | `String` / `GoString` render `"<redacted>"`, asserted under `%v`, `%s`, `%#v`, `%+v`, `%q` and inside a struct |
| Has exactly one exit | `Reveal()`, deliberately spelled to read like a mistake anywhere that is not building a cookie |
| Compared in constant time | `Equal` uses `crypto/subtle`; every store path ends in `digestsEqual`, also `crypto/subtle` |
| Never a map key, a filename, or at rest | stores index by `Digest()` — the unkeyed SHA-256 — and re-attach the identifier the *caller* presented |
| Never in an error | no `Public`, `Private` or `Field` in this domain can carry one. A `Public` is read by third parties; that is the `errs` split applied to the same problem |

Naming files by digest is what makes a stolen backup useless: it yields digests,
not cookies. `TestNoIdentifierIsEverOnDisk` walks the entire store directory —
filenames included — and fails on a match.

**Length is fixed, not configurable.** Every value below 32 bytes is a weaker
session and every value above it only lengthens the cookie, so there is nothing
for a knob to express.

### D4 — expiry is absolute AND sliding, and the earlier deadline always wins

`Config` requires **both** `IdleTimeout` (refreshed by every `Load`) and
`AbsoluteTimeout` (from the instant the identifier was minted). The effective
deadline is `min(createdAt+Absolute, lastSeen+Idle)` — one sentence, no
exceptions, no averaging, no preferring the later one.

The two contradict constantly; that is the normal case. Three answers make the
rule true rather than aspirational:

1. **The store clamps on write.** `window.slide` never records an idle deadline
   past the ceiling, so nothing stored can outlive it.
2. **The value type clamps on construction.** `NewSessionValue` clamps again, so
   rule holds for a `SessionValue` a *third-party* store built without knowing
   about it. The invariant belongs to the type, not to this package.
3. **The configuration is refused when the sliding half could never bite.**
   `IdleTimeout >= AbsoluteTimeout` makes the idle window unreachable — a
   sliding expiry the caller believes they configured and does not have. Refused
   at construction with `InvalidConfig`.

A non-positive timeout is likewise **refused, not clamped** (ADR 0031). There is
no default the SDK could invent: how long a session may idle, and how long it
may live at all, are the entire content of a session policy. Reading `0` as
"expires immediately" would produce a store in which every session is already
dead — a login loop with no error message anywhere, which is the worst way a
misconfiguration can present.

**Rotating does not buy time.** `Regenerate` with the *same* subject keeps
`createdAt` and the ceiling: otherwise a caller rotating on a timer would hold an
immortal session and the absolute timeout would be decorative. A rotation that
*changes* the subject restarts both clocks, because a privilege boundary
produces a genuinely new session and handing it the remaining seconds of the
anonymous browsing before it would log a user out moments after they signed in.

### D5 — the file store makes real guarantees, or refuses

Five guarantees, each with a mechanism and a test:

| Guarantee | Mechanism |
|---|---|
| Confidentiality at rest | every record is an AES-256-GCM box (ADR 0013), never a readable frame |
| No identifier on disk | filenames and records carry `ID.Digest()` only |
| No record substitution | the seal's additional authenticated data binds each record to its own filename, so moving one record onto another's digest yields a value that does not open |
| Owner-only permissions | directory `0700`, records `0600` — **narrowed and then `stat`'ed** |
| Atomic publication | write beside, `Sync`, `rename(2)`; a failed write removes the orphan and leaves the previous record byte-for-byte intact |
| Serialised read-modify-write | one store-wide exclusive `flock` per operation |

Two of those choices deserve their reasoning recorded.

**Requesting a mode is not getting one.** Three different things widen it: a
default POSIX ACL on the parent (this repository's own devcontainer hands back
`0775` from `t.TempDir()`), a filesystem that does not implement Unix
permissions at all (exFAT, SMB, a container mount with a blanket `file_mode=`),
and an operator's pre-existing `0755` directory. So the store **chmods what it
created, refuses what it did not, and stats either way**. Narrowing an
operator's directory — possibly shared with another service — is not the SDK's
decision; refusing it is. And the `stat` after the `chmod` is what catches the
filesystem that accepts the call and changes nothing. *Verify the invariant; do
not assume it because the code looks right.*

**One store-wide lock, not one per record.** Per-record locking needs a lock
file per record, and a lock file that is ever unlinked has a well-known race: a
process blocked on the old inode acquires it just as another creates and locks a
new one, leaving two holders. Never unlinking them leaks a file per session. One
descriptor, opened at construction, held for microseconds per operation, avoids
both. `Load` takes it *exclusively*, because `Load` writes — sliding the idle
window is a write, and that cost is stated rather than hidden.

**Where the mechanics do not exist, the constructor refuses** with the SDK-wide
`proc.UnsupportedPlatform` (ADR 0018 §(a)) — at construction, so the refusal
arrives where the program is wired rather than at the first login. Native on
linux, darwin, freebsd, openbsd, netbsd and dragonfly. Refused on Windows, and
on every `GOOS` whose stdlib `syscall` has no `Flock` (wasip1, solaris, illumos,
aix). Verified by cross-compiling the package for each of them; plan9 and js are
outside the SDK's build matrix for a pre-existing reason in `internal/core/proc`
that this domain neither introduces nor fixes.

Windows is the interesting refusal. `os.Chmod` there maps a `FileMode` to the
read-only attribute and nothing else, so `0600` does not describe an ACL and a
file created under an inheritable permissive DACL is readable by whoever that
DACL admits. Doing it properly means `CreateFileW` with a `SECURITY_ATTRIBUTES`
security descriptor, which stdlib `syscall` does not expose and which
`internal/service` cannot reach through `x/sys` (banned, ADR 0022/0034). The gap
has a known closure — `advapi32` via `syscall.NewLazyDLL` — and is deliberately
deferred, because ADR 0018's **runtime bar** is not cleared by a green
cross-compile, and an untested implementation of a security boundary is worth
less than an honest refusal. Both `fsguard_*.go` files compile on every `GOOS`,
so the **build bar** is clear today.

### D6 — the frontier: the SDK ships the store and the sealing, nothing else

**The firewall, the voters, the login conventions and the rendering of the
cookie on an HTTP response belong to the framework.**

`Sealer` produces the cookie's **value** — an AEAD box over the identifier,
bound to a required purpose string, rendered unpadded base64url so every
character is in RFC 6265's cookie-octet set. It does not write a cookie. Nothing
in this domain imports `net/http`, and nothing in it should.

| Ours | Not ours |
|---|---|
| minting, storing, expiring, rotating, destroying | deciding *when* to do any of them |
| the cookie's value | its name, `Path`, `Domain`, `Max-Age`, `Secure`, `HttpOnly`, `SameSite`, and `http.SetCookie` |
| `Subject()` — the principal a store recorded | what that principal may do |
| the typed verdicts and their HTTP statuses | turning a verdict into a redirect, a challenge or a 403 page |
| refusing a write that would be fixation | knowing that a request is a login |

A domain that knew what a request was would have to know what a route, a
middleware and a user model were, and each of those is a framework's opinion.
The line is written into three `CLAUDE.md` files and into the package doc
because it is the only thing that will keep this domain from becoming an
authentication framework by accretion.

`Sealer.Open` returning an identifier is **not** an authentication. It proves
only that somebody holding the key minted the value; whether it names a live
session is `Store.Load`'s answer, and a framework that stopped at `Open` would
have built an authentication bypass out of an expired cookie.
`TestOpeningIsNotAuthorising` says so executably.

## Consequences

- **14th core sibling**; the `internal/core` purpose statement widens. Error
  blocks `0.2.14.*` (port) and `0.3.46.*` (stores), allocated in
  `codeRangeOwners` in the same change (ADR 0035).
- **Every "no usable session" verdict is HTTP 401** — `NotFound`, `Expired`,
  `InvalidID`, `SealInvalid`, `RecordCorrupt` — so a framework routes the family
  with one `errs.HTTPStatusOf` instead of a switch it must keep in sync.
  `StoreUnavailable` is 503 + `EX_TEMPFAIL`; `InvalidConfig`, `DirectoryUnsafe`
  and `InvalidPurpose` carry `EX_CONFIG`.
- **`NotFound` and `Expired` stay distinct.** They need different operator
  responses — a spike in `Expired` is a timeout that is too short, a spike in
  `NotFound` is a store losing records — and telling them apart leaks nothing,
  because a 256-bit identifier cannot be reached by guessing.
- **A broken entropy source is refused, never worked around.** A minted
  identifier that already names a live session is `IdentifierCollision` at both
  `New` and `Regenerate`. The second is the important one: a rotation that
  "succeeded" onto the same identifier would be a login that did not rotate,
  silently reinstating D2's hole.
- **Rotating the file store's key logs everyone out**, and `Sweep` removes the
  now-unreadable records. Stated, not discovered.
- **Nothing in the domain sleeps.** Both stores take `clock.Clock` — the *narrow*
  half of ADR 0039's split, because a store reads time and never waits on it —
  so a 12-hour ceiling is asserted in microseconds on a `ManualClock`.
- **The at-rest frame is hand-written, not dispatched through `codec`.** It is
  storage, not interchange: nothing outside the package reads it, it must be
  total over `map[string]string` with no reflection and no tag vocabulary, and
  routing it through the registry would drag a codec into every binary that
  wants a session store. It is versioned, deterministic (sorted keys), and
  bounded before it allocates.
- **Payloads are capped** at 256 keys and 4 KiB per key or value, refused with
  `PayloadTooLarge` rather than truncated. A session store is per-user state on
  the request path, not a database.

## Why not …

- **… reuse `token` for sessions.** A token cannot be revoked. That is the whole
  reason both exist.
- **… a registry keyed on a store name.** The set is closed and the choice is
  not data. A typo must not be able to swap a persistent store for an ephemeral
  one.
- **… a six-method `Store` with `Sweep` inside it.** ADR 0039. It would also
  force every third-party store to implement a sweep it may have no way to
  perform.
- **… let `Regenerate` always restart the absolute clock.** A caller rotating on
  a timer would hold an immortal session.
- **… let `Regenerate` never restart it.** A user would be logged out moments
  after signing in, at the end of the anonymous browsing that preceded them.
- **… ship a Windows file store that calls `os.Chmod` and reports success.**
  That is the store that pretends. Refuse instead, and record the closure.
- **… a `FromRequest` / `SetCookie` helper "for convenience".** That is the
  first step across the frontier, and it is always the one that looks harmless.
- **… make `Reveal` a friendlier name.** Its awkwardness at a call site is a
  feature.
- **… distinguish the causes of `SealInvalid` or `RecordCorrupt`.** One verdict
  for tampering, truncation, a wrong key and a wrong purpose is what keeps them
  from becoming oracles — the posture `crypto.DecryptionFailed` already takes,
  carried one layer up rather than undone here.

## Deferred

- **A Windows file store** behind a real DACL (`CreateFileW` +
  `SECURITY_ATTRIBUTES` via `advapi32`), validated on a real kernel by
  `e2e-vm.yml` per ADR 0018's runtime bar.
- **A distributed store** (Redis, Postgres). `Store` is a published port and
  `StateValue` exists precisely so one can be written outside the SDK; shipping
  one here would put a vendor dependency in `internal/service`.
- **Concurrency-optimised file locking** — a shared lock for a read-only `Load`
  path, if a caller ever wants an idle window that does not slide.
- **Session events** (created / rotated / destroyed) for audit. It is a real
  need and it is an observability decision, not a storage one; it belongs beside
  `metrics` (ADR 0027) rather than inside the port.

## References

- OWASP Session Management Cheat Sheet — session fixation, identifier entropy,
  idle vs absolute timeout.
- RFC 6265 §4.1.1 — `cookie-octet`, which is why the sealed value is unpadded
  base64url.
- `internal/core/session/CLAUDE.md` — the port, the frontier table, the four
  security properties.
- `internal/service/session/CLAUDE.md` — the platform matrix, the
  narrow-then-assert rule, and the store-wide lock rationale.
- `pkg/v1/session/README.md` — the consumer-facing surface (generated, ADR 0008).
