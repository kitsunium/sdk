# internal/service/entitlement/

## Purpose

The engine behind `pkg/v1/entitlement` (ADR 0079): fetch a vendor-signed roster,
authenticate it, decide whether this machine is entitled, and keep deciding when
the network is gone. Implements `internal/core/entitlement`'s contract; consumers
import the facade, never this package.

Every vendor-specific fact — origins, cache directory, OIDC audience, enrolment
URL — lives in `ProductValue`, which is why one implementation serves every
product (ADR 0078 §1).

## Contents

| File | Role |
|---|---|
| `service.go` | the `Service` handle, `Verify`, the origin fallback, `matchSubject` |
| `anchors.go` | the ORDERED list of vendor keys this verifier accepts, its bound, and the two readers |
| `roster_parse.go` | `ParseRoster` — two documents, raw + detached signature |
| `bundle.go` | `ParseBundle` — the one-document form the cache stores |
| `cache.go` | the offline copy and the anti-rollback ratchet, over storage AND acceptance |
| `cache_lock.go` | exclusion over the cache directory — what rename does not give |
| `ci.go` / `ciseat.go` | GitHub Actions OIDC: mint, verify, then look up the seat |
| `jwks.go` | the issuer's published RSA keys |
| `oidc.go` | the token's claim set and its strict decoding |
| `roughtime*.go` | signed network time — advisory, fail-open, servers ship empty |
| `version.go` | the version floor a roster can mandate |
| `product.go` | `ProductValue`, `Label`, `DefaultCacheDir`, `Validate` |
| `errors.go` | the one code this implementation owns, `0.3.67.*`, and its sentinel |
| `wrap.go` | `refuse` / `classify` / `classifyForeign` / `annotate`, plus `diagnose` |

## Why-this-shape

- **The anchor is a LIST, and one key had no way out.** `Service.vendor []byte`
  was one key and every path read it, so an installation whose anchor had to
  change was blocked from both sides: a roster signed by a new key B came back
  `ErrRosterUnsigned` — the SPOOFING sentinel, from a document the vendor
  genuinely signed — and it came back in `rosterFrom`, which `Verify` reaches
  BEFORE `RequiresUpdate`, so the update floor that exists to say "upgrade this
  binary" was never read; while a release signed by B was refused by the anchor A
  the running binary still held. `anchors` is now an ordered list and a roster is
  authentic against ANY entry, which is the whole of rotation. Several keys are
  still ONE signer — no quorum is taken, because authentication establishes only
  that the vendor issued these bytes. The cost is real and is stated rather than
  mitigated: an anchor on the list is a key whose compromise is ACCEPTED while it
  is listed, and what bounds it is that the list is ordered, capped at
  `maxAnchors` and a BUILD decision no runtime path can extend. ADR 0091.

- **The anchor loop obeys the ORIGIN loop's rule, and for the same reason.**
  `parseBundleAnyAnchor` moves on from a document an anchor cannot AUTHENTICATE
  and never from one it authenticated and the rules then refused. Only
  `ErrRosterUnsigned` is a question about the key; an undecodable bundle, a
  duplicated member name and a window that is over-wide, unopened or closed are
  properties of the DOCUMENT and identical under every anchor. Continuing past
  one of those would report an expired roster as a forgery, sending an operator
  whose publisher had merely fallen behind to look for an impersonator. The
  ratchet reads its mark through the same list, because dropping the mark at a
  rotation hands back the whole rollback distance at the one moment an
  installation cannot re-establish it.

- **The possession proof is bound to the fingerprint the roster AUTHORISED,
  when the port can accept it.** `matchSubject` used to ask `Fingerprint` what
  the machine presents, compare the answer itself, and then ask
  `ProvePossession` — so the authorised value never crossed the port and an
  implementation signed with whatever the second call found. A rotation landing
  mid-verification, a remounted volume or a swapped `<uuid>.pub` was signed for.
  `(*Service).prove` prefers `coreent.BoundProver` by type assertion and falls
  back to the three-method call, which is ADR 0052's `Deadliner` mechanism: the
  absence is how a caller discovers which world it is in. The fallback is not a
  weakening and not optional — every implementation written against the frozen
  port lands there. The engine's own comparison STAYS: it is the cheap gate
  before the signing round trip, and it is what produces `ErrKeyMismatch` for an
  identity that cannot bind. ADR 0092.

- **The order in `Verify` is the security property.** Authenticate the roster
  against the vendor key, then check its window, then match the subject, then
  demand a possession proof. Every gate before the last one reads material the
  roster hands to everyone; only possession distinguishes the holder.
- **Offline is a fallback, never a bypass.** `cachedRoster` replays a bundle
  this machine already authenticated, and it re-runs the identical signature and
  window checks. What it cannot re-run is the clock, which is why the ratchet
  exists — and why the frozen-clock hole is documented as open rather than
  claimed closed.

- **The ratchet guards ACCEPTANCE, and it used to guard only storage.**
  `rememberRoster` refused to CACHE a roster older than the mark, and
  `rosterFrom` handed that same roster to the decision anyway — `Verify` never
  compared the roster in its hand against the mark. So a genuine roster signed
  before a revocation, replayed by any origin, was denied a place in the cache and
  granted a seat at the table: it restored a revoked subject, a rotated-out
  fingerprint, a lower version floor, a withdrawn CI account, and `CIRelaxed`
  itself — the policy was replayable, which is the half no signature can defend.
  `rememberRoster` now RETURNS the verdict, `judgeMark` reaches it, and the
  comparison and the install happen under one hold because a comparison whose
  result outlives the state it was taken against has decided nothing. The refusal
  travels as an ordinary origin failure so the loop moves to the next publication
  point: one lagging mirror must not cost a licence.

- **Equality of `IssuedAt` is accepted, and only because of the digest.** Every
  re-verification inside one publication interval offers the roster already
  cached, so refusing an equal instant outright would refuse most verifications.
  `markRecord` therefore carries a SHA-256 of the signed PAYLOAD — not of the
  bundle, because two bundles differing only in their outer JSON are one statement
  — and two payloads that differ at one instant are two statements, where the one
  already accepted wins.

- **The CACHE path is exempt from the comparison, by choice.** `cachedRoster`
  reads the same file `markWhileHeld` reads, so comparing what comes back against
  the mark derived from it can only ever say "equal, same payload". The one way it
  could say otherwise is a concurrent refresh between the two reads — and refusing
  there would turn lock contention into a refused licence. The guarantee is about
  what an ORIGIN can undo.

- **And the guarantee is CONDITIONAL, stated as such everywhere it appears.** The
  mark is the cached bundle's `IssuedAt` and nothing else, so anti-replay lapses
  when there is no cache (`cacheDir == ""`, which is every injected-getter
  construction), when the install failed, and when the guard stood down. The last
  two log; none of the three refuses instead. Do not restate it as unconditional
  in a README or a doc comment — that was the class of defect this audit closed.

- **A document that is not a roster proves nothing about time.** `authenticateBundle`
  authenticated and returned, so the mark advanced on anything the vendor had
  signed that carried an `iat` — including a document `ParseRoster` refuses to
  AUTHORISE on its own shape, such as one whose window is wider than
  `RosterLifetime`. `rosterMark` applies the clock-FREE half of ParseRoster's
  judgement before accepting a mark: a non-zero `iat`, a window that is not
  inverted, a width within `RosterLifetime`. No clock, because the ratchet's input
  is normally expired.

- **`markCeiling` refuses in BOTH directions, and that is the whole of it.** A
  vendor-signed bundle dated far in the future, written straight into the cache
  directory, sets the mark and gets the machine refused on a clock that is
  correct — where "set your clock" is the wrong advice. The repair is a SECOND
  `condition` under the SAME `ErrClockRegressed`, never a discarded mark: ignoring
  an implausible mark would hand over the ratchet's own bypass, since rolling the
  clock back past `markCeiling` puts the LEGITIMATE mark outside the ceiling too.
  `Test_Service_checkClock_refusesBothDirectionsOfAnImplausibleMark` fails on both
  mistakes.
- **`VerifyCI` takes the roster as a signed document, not as an interface.**
  GitHub's word is that the run is real, not that it is paid for. Entitlement is
  a property of the vendor's roster, and the party being checked must not be able
  to supply the type that answers it. See `.ktn-linter.yaml`'s KTN-API-MINIF
  entry for why the narrowing the linter suggests is refused here.
- **The origin loop moves on from a document it cannot USE, never from one it
  can use and does not like.** So the first origin serving a roster that verifies,
  is in window and is not a replay ends the search — including a roster that
  entitles nobody, which is how one mirror shipping an empty `subjects` map
  revokes every machine reading it while a healthy mirror goes unconsulted.
  Continuing until an origin AUTHORISES is the mirror-image defect and far worse:
  any configured endpoint could then veto a revocation. The narrower reading — "an
  empty roster means the publisher broke" — is not the client's to assume either:
  an authentic empty roster IS a vendor statement, and re-reading it as a fault
  would have to travel in the signed document, the way `CIRelaxed` does. That is a
  product decision; until it is taken,
  `Test_Service_currentRoster_stopsAtTheFirstUsableRoster` pins the limit so it
  cannot move by accident.

- **A CI seat is bounded by its own token.** `ciseat.go` passed
  `GrantDeadline` the roster's window and the account's term — the two bounds a
  DEVICE grant rests on — and never `claims.ExpiresAt`, so a seat established by a
  thirty-minute proof outlived it by up to a day, against `core/grant.go`'s own
  stated invariant that "a grant may not outlive the document that authorised it".
  `GrantDeadline` is variadic now so a third document can be named. No `clockSkew`
  is added: `checkTiming` allows it to ADMIT a token, which is the permissive
  direction, and applying it to a BOUND would extend the grant past the proof.

- **There is no key-set cache, and therefore no refresh to implement.** Two
  comments promised one: `selectKey` said "the caller refreshes the JWKS once and
  retries" and `coreent.ErrCIUnknownKey` said "refresh the key set once, since
  GitHub rotates keys, then refuse if it still does not appear". **No caller did
  either** — `ciSeat` fetches once, calls `VerifyCI`, propagates. Both claims are
  gone, and what replaced them is the true statement: `publishedJWKS` reads the
  published set on EVERY verification, so a rotation is picked up by the next
  verification with no cache to invalidate and no process to restart. Building
  the promised refresh would be worse than the gap it appears to close — a second
  GET of the same URL seconds later returns the same bytes and cannot make an
  endpoint publish a kid it does not publish; it would let a TOKEN cause a
  request, which nothing can do today because the fetch precedes any token
  (measured at 2× in the mutation that added it); and it is the repair for a
  stale CACHE, so it would have to introduce the staleness it then fixes.
  `Test_Service_ciSeat_fetchesTheKeySetOncePerVerification`,
  `Test_Service_ciSeat_doesNotAmplifyARepeatedUnknownKid` and
  `Test_Service_ciSeat_picksUpARotationOnTheNextVerification` are what keep those
  paragraphs answerable to the code — the last one turns red the day the cache
  arrives.

- **Three smaller claims corrected in the same sweep, each overstated in the
  same direction.** `jku`/`x5u` are refused on a NON-EMPTY value: absent, `""`
  and `null` all decode to the empty string and are indistinguishable, and the
  comment said "their presence can be refused". Harmless — neither field chooses
  a key source here — and still not what the code does. `RepositoryOwnerID` is
  called numeric because that is what GitHub SENDS; nothing validates it, the only
  check is non-empty and the comparison against the roster's keys is exact, so a
  decimal check would be a second syntax for a comparison that already is one.
  (Checked and found already correct: a JWT header with an empty `kid` is
  refused, a JWKS entry without one is skipped, and each site says so where it
  happens.)

- **A CI failure is not a refusal unless the roster says so.** A runner that
  also holds a device key must keep working, so `ciSeat`'s failure falls through
  — except when `ciRefusalIsFinal`, which is the only place "this run must be CI"
  can be stated without letting the party being checked state it.
- **The ratchet is a compare-and-install, so it needs exclusion.** Reading the
  high-water mark and renaming a bundle over it are two filesystem operations.
  Without a lock between them two writers both read the same mark, both
  conclude they are newer, and whichever renames LAST sets it — measured at 183
  of 400 rounds, and what it loses is anti-rollback distance, since `checkClock`
  refuses a clock earlier than that mark. `holdCache` makes the pair one
  operation; `markWhileHeld` is the read a caller already holding it uses,
  because the guard is not reentrant.
- **Exclusion is not the whole ratchet, because a write can be SKIPPED.**
  `holdCacheForWrite` stands down when the guard is held elsewhere, and standing
  down costs nothing to the CACHE and everything to the MARK: the holder is
  installing a roster, not this one. Measured on `windows-latest`, run
  34782571674 — one stand-down, one round in 400 ending below the newest
  generation. `rememberRoster` therefore raises an in-process floor under the
  mark BEFORE it reaches the guard, and `signedHighWaterMark` returns the later
  of the floor and the disk. The same floor covers the three non-racy ways this
  package can authenticate an instant it cannot install: no lock on the
  platform, an unwritable cache directory, a failed write. It does not persist,
  and the ratchet's own comparison still reads the disk — see `raiseMarkFloor`
  for both reasons.
- **Readers take the same guard, and only Windows needs them to.** A rename is
  atomic for a POSIX reader, so nothing there requires it. Windows refuses to
  replace a file another handle holds open, and `syscall.Open` never asks for
  `FILE_SHARE_DELETE` — so the cache's own reader, in a second process, is what
  makes a refresh fail. Sharing DELETE is not a way out: measured on
  `windows-latest`, `MoveFileEx` still refuses. A holder this package does not
  control — antivirus, backup, indexer — still can, and that residue is
  accepted and logged rather than retried past.
- **The public sentence names no particular, and that is the point.** Every
  refusal here is built with `refuse` or `classify` over one of the fifteen
  contract sentinels, so `err.Error()` is the wire-safe half and nothing else:
  no url, no host, no path, no subject, no kid. Where it happened travels in
  `Fields`, and `particulars` reads it back.
  `TestNoParticularReachesThePublicSentence` asserts both directions on six
  refusals, because leaking a particular and losing it are both defects and
  only one of them is the one everybody remembers.

- **Origin-wins is the wrong rule at a consumer seam.** A Getter, a BearerFetch
  and a response Body are not a deeper layer of this SDK — they are somebody
  else's package, free to return an `*errs.Error` from a code range nobody here
  allocated, and `errs.Wrap` would make it the identity of a roster outage.
  `classifyForeign` hides it from origin-wins and leaves it matchable by
  `errors.Is`. Measured: plain `classify` gives
  `errors.Is(err, ErrRosterUnreachable)=false` and `code=0.3.48.1`.

- **`annotate` guards, and the guard is not defensive.** `errs.Wrap` has no
  spelling for "add a field, decide nothing": zero `WrapParams` over a cause
  carrying no `*errs.Error` returns `CodeInvalidWrapParams`. `Identity` is a
  PORT, so a consumer's plain `errors.New` reaches `ciContext` — and would have
  been replaced by "internal wrap failure" on the one path that reports it.

- **`readCappedFile` and `readBounded` exist because the inputs are hostile.**
  Everything here parses bytes fetched from the network or read from a cache an
  attacker may have written, before any signature has vouched for them.

- **The nil-tolerance contract covers the CONSTRUCTORS too, and it did not.**
  Every `ProductValue` accessor tolerates a nil receiver, and so does `Validate`
  — but `NewService` and `NewServiceWithGetter` read the `Origins` FIELD, which
  no method can guard, so `New(identity, vendor, nil)` panicked on exactly the
  path that runs when a consumer has configured nothing yet. Both now go through
  `PublishedOrigins()`. A product publishing nowhere yields a verifier that
  refuses with `RosterUnreachable`, which a caller can handle.

## Known debt

A cache refresh can still be refused by a file holder outside this process —
antivirus, backup, a search indexer on Windows. `holdCache` excludes every
holder that takes the same lock, which is every one this SDK controls, and no
lock reaches the others. The refusal is logged and the next invocation retries
it.

## Do NOT

- Build an error with `fmt.Errorf`. There were 112 of them and there are none;
  the three shapes in `wrap.go` are what a call site chooses between, and the
  package is in `//:audit_sources` so a code that strays out of `0.3.67.*`
  fails the build.
- Spell a cause as `cause.Error()`. After the conversion that is the wire-safe
  sentence and carries no url and no syscall text — `publishedJWKS` was one
  edit away from swapping its diagnosis for a tautology. Use `particulars`.
- Name a roughtime server here. The list is a deployment decision, and shipping
  one would make every consumer depend on a host the SDK does not operate.
- Add an ssh import. The identity is a port; its ssh implementation lives in
  `third-party/entitlement` for the reason ADR 0079 measures.
- Collapse `RosterUnreachable` into a refusal. It says "cannot decide", and
  reporting an outage as a revocation is the one wrong answer.
- Read the anchor list anywhere but `parseBundleAnyAnchor` / `bundleMarkAnyAnchor`,
  or let a roster field, an environment variable or the cache add to it. The list
  being a BUILD decision is the whole of what bounds the surface it costs, and a
  document signed by one listed key cannot be what decides which keys are listed.
- Give the empty list a code of its own. It refuses under `ErrRosterUnsigned`
  with its own `condition`, for the reason the sixteenth code was refused.
- Make `markCeiling` discard a mark instead of refusing on it, or move the
  ceiling into `rosterMark` where there is no clock. Both are the same bypass.
- Apply the anti-replay comparison to `cachedRoster`, or describe the guarantee
  as unconditional. Two different mistakes, both of them a sentence that outruns
  the code.
- Add a JWKS cache, or a refresh keyed on an unknown `kid`, without reading the
  three tests named above first. The absence is the mechanism: it is what makes a
  rotation a non-event, and the refresh is the repair for the staleness only a
  cache introduces.
- Restore either retired claim about refreshing the key set. A sentinel or a
  comment that advertises a remedy nobody implements is the defect this sweep
  closed, not a wording preference.
- Write a doc comment describing a POLICY for a claim nothing reads.
  `event_name` and `runner_environment` carried one for months, four lines under
  a type comment promising the opposite;
  `Test_VerifyActionsToken_ignoresTheClaimsNothingReads` is what keeps the
  silence mechanical now.

## Verification

```sh
bazel test --config=race //internal/service/entitlement:entitlement_test
```
