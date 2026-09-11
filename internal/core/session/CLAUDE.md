# internal/core/session/

## Purpose

Declares the **server-side session port**: `Store` (who owns a session's
lifetime), `Sealer` (how its identifier becomes a cookie value), the opaque
redacting `ID`, and the immutable `SessionValue` a store hands back. The 14th
core sibling, admitted by **ADR 0045**. Both concrete stores — memory and file —
and the AEAD sealer live in `internal/service/session`.

Code range: `0.2.14.*` (ADR 0045).

## Contents

| File | Surface |
|---|---|
| `session.go` | package doc + `Store` (5 methods, FROZEN) + `Sweeper` (the first sibling) |
| `sealer.go` | `Sealer` — `Seal(ID) (string, error)` / `Open(string) (ID, error)` |
| `id.go` | `ID` + `IDLen` + `NewID` / `ParseID` (strict base64url, one spelling); `Reveal` / `Digest` / `Equal` / `IsZero` / redacting `String` + `GoString` |
| `session_value.go` | `SessionValue` + `NewSessionValue` (its only producer) — accessors, copy-on-write `Set` / `Delete`, `ExpiresAt` / `LiveAt`, shape-only `String` |
| `state_value.go` | `StateValue` — the exported description a third-party `Store` hands to `NewSessionValue` |
| `codes.go` | `Code*` constants — range 0.2.14.* |
| `errors.go` | `NotFound` / `Expired` / `InvalidID` / `InvalidConfig` / `IdentifierCollision` / `EntropyFailed` / `StoreUnavailable` / `SealInvalid` / `FixationRefused` |

## A session is not a token — say it before anything else

`internal/service/token` (ADR 0042) and this domain are constantly confused, and
the confusion is expensive because the two differ on exactly the property people
assume they share.

| | Token (JWT / PASETO) | Session |
|---|---|---|
| Where the facts live | inside the value, signed | on the server, in a `Store` |
| What verification needs | a key | a key *and a lookup* |
| Revocation | **impossible** — only expiry ends it | `Destroy`, effective on the next request |
| Scales across processes sharing nothing | yes | only as far as the store does |
| What the value reveals | its claims, to anyone who base64-decodes it | nothing at all |

"Log out everywhere" means *wait* for a token and *now* for a session. That one
row is usually the whole decision.

## The frontier — where this domain stops

**The SDK ships the magasin and the scellement. The firewall, the voters, the
login conventions and the rendering of the cookie on an HTTP response belong to
the framework.**

That sentence is the reason this package exists in the shape it does, and it is
what keeps `session` from growing into an authentication framework. Concretely:

| Ours | Not ours |
|---|---|
| minting, storing, expiring, rotating and destroying a session | deciding *when* to do any of them |
| producing the cookie's **value** (`Sealer.Seal`) | the cookie's name, `Path`, `Domain`, `Max-Age`, `Secure`, `HttpOnly`, `SameSite`, and the `http.SetCookie` call |
| `SessionValue.Subject()` — the principal a store recorded | what a principal is allowed to do |
| the typed verdicts (`NotFound`, `Expired`, …) and their HTTP statuses | turning a verdict into a redirect, a challenge, or a 403 page |
| refusing a write that would be session fixation | knowing that a request is a login |

Nothing here imports `net/http`, and nothing here should. A domain that knew what
a request was would have to know what a route, a middleware and a user model
were, and every one of those is a framework's opinion.

## The four security properties, and where each one lives

**1. Fixation — `Regenerate` is not a step you can forget.** The only call that
binds a subject is `Store.Regenerate`, and it mints a new identifier every time.
There is no `SetSubject`, no `SessionValue.WithSubject`, and `Store.Save` never
writes a subject — it refuses a mismatch with `FixationRefused`. "Keep this
identifier and log this user in" is a sentence with no method. Two executable
guards hold it: `TestRegenerateIsTheOnlyWayToNameASubject` (reflection over the
port: no method but `Regenerate` takes a string) and
`TestSessionValueHasNoSubjectMutator`.

**2. The identifier is a secret, and the type behaves like one.** 32 bytes from
`crypto/rand`. `String`/`GoString` render `"<redacted>"`; `Reveal` is the only
exit and is spelled to read like a mistake outside a cookie; `Equal` uses
`crypto/subtle`; `Digest` is the SHA-256 a store indexes and names files by, so
the secret is never a map key, never a filename and never at rest. `ParseID`
decodes STRICTLY: 43 characters carry 258 bits for 256, and a lenient decoder
would accept all four settings of the two spare bits as the same identifier —
one session, four cookies. No `Public`,
no `Private` and no `Field` in this domain ever carries one — a `Public` is read
by third parties, which is the `errs` split applied to the same problem.

**3. Expiry is both absolute and sliding, and the earlier deadline always
wins.** `SessionValue.ExpiresAt` returns `min(absoluteExpiry, idleExpiry)`, and
`NewSessionValue` **clamps** the idle deadline to the ceiling so the rule holds
every value in existence, including one a third-party store built. A sliding
window with no ceiling never dies; a ceiling a sliding window can outrun is
decorative. The service layer refuses `IdleTimeout >= AbsoluteTimeout` at
construction for the same reason (ADR 0031).

**4. Real OS behaviour, or an honest refusal.** Not this package's problem —
the port is platform-neutral by construction. See
`internal/service/session/CLAUDE.md` §Platform matrix.

## Conventions

- **No registry.** Like `proc` (ADR 0016), `resilience` (ADR 0026), `net`
  (ADR 0029), `scheduler` (ADR 0041) and `token` (ADR 0042). The set of stores
  is closed and the choice is a deployment decision made in code; resolving it
  from a configuration string would let a typo silently downgrade a persistent
  store to an ephemeral one.
- **`Store` is FROZEN at five methods.** `pkg/v1/session` aliases it, so the
  shape is published and Go interfaces are structural: a sixth method breaks
  every downstream implementer at compile time (ADR 0039). New capabilities are
  **sibling interfaces reached by type assertion** — the model is codec's
  `Appender` (ADR 0037). `Sweeper` is the first; `io.Closer` on the file store
  is the second and needed no declaration at all.
- **`Regenerate` takes the subject, and every other method does not.** That is
  not stylistic — it is the property the reflection guard checks.
- **`StateValue` is exported so the port is implementable.** Every field of
  `SessionValue` is unexported, so without it `Store` would be published and
  unimplementable. It is not a fixation back door: the forged session is
  constructible and *not persistable*, which is what makes `FixationRefused` a
  runtime verdict a test can reach rather than a comment.
- **`NotFound` and `Expired` are distinct on purpose.** They need different
  operator responses — a spike in `Expired` is a timeout that is too short, a
  spike in `NotFound` is a store losing records — and telling them apart leaks
  nothing an attacker can use, since an identifier is 256 random bits and cannot
  be reached by guessing.
- **Every "no usable session" verdict is HTTP 401.** `NotFound`, `Expired`,
  `InvalidID`, `SealInvalid` and (in service) `RecordCorrupt` all map to it, so a
  framework routes the family with one `errs.HTTPStatusOf` instead of a switch it
  has to keep in sync. `StoreUnavailable` is 503 + `EX_TEMPFAIL`;
  `InvalidConfig` carries `EX_CONFIG`.

## Do NOT

- **Add a method to `Store` or `Sealer`.** Both are aliased by `pkg/v1/session`
  (ADR 0039). Add a sibling interface and reach it by type assertion.
- **Add a subject mutator anywhere.** Not on `SessionValue`, not on `Store`. The
  reflection guards fail first, and they are there because a comment would not
  have survived.
- **Put a `Public`, a `Private` or a `Field` in this package that could carry an
  identifier, a subject, a data key or a path.** A `Public` is read by third
  parties. `Digest` exists precisely so a log line can correlate without a
  secret.
- **Import `net/http`, or anything that knows what a request is.** See §The
  frontier.
- **Make `Reveal` convenient.** Its awkwardness at a call site is the point.
- **Let `ExpiresAt` prefer the later deadline, average the two, or special-case
  their equality.** The rule is one sentence with no exceptions.

## Verification

```
bazel test --config=race //internal/core/session:session_test
# OR
cd internal/core && GOWORK=off go test -race -cover ./session
# coverage today: ~93% (the uncovered statements are accessor branches the
# service-layer suite exercises end to end)
```

| File | Covers |
|---|---|
| `id_external_test.go` | length refusal, the single accepted spelling — including a respelling through the unused trailing bits, proved to decode to the same bytes under a lenient decoder before it is refused — cookie-safety of `Reveal`, `Digest` against an independently computed SHA-256, `Equal` at every length, and redaction under `%v` / `%s` / `%#v` / `%+v` / `%q` and inside a struct |
| `session_value_external_test.go` | the earlier-deadline rule in both directions, the `NewSessionValue` clamp, the exclusive `LiveAt` boundary, copy-on-write `Set`/`Delete`/`Data`, sorted `Keys`, and shape-only rendering |
| `port_external_test.go` | the two fixation guards and the ADR 0039 freeze — `Store` is exactly five methods, `Sweeper` is separate, and `Store` must NOT satisfy it |
