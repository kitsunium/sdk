<!-- updated: 2026-10-03T03:30:00Z -->
# framework/connectors/ssh/

## Purpose

The **ssh implementation of `framework/entitlement.Identity`**, plus
enrolment: minting a subject identity locally and turning it into a request the
vendor can act on (ADR 0079).

The mechanism this identity feeds — roster, signature, offline cache,
anti-rollback ratchet, CI seat, version floor — is NOT here. It lives in
`framework/internal/service/entitlement` behind the public `framework/entitlement` package,
and importing that package brings no ssh code and no `golang.org/x/sys` into a
consumer's graph.

**A Go module of its own** (`github.com/kitsunium/sdk/framework/connectors/ssh`,
ADR 0158 §3), the shape the database engines beside it have (ADR 0147 §7):
`golang.org/x/crypto/ssh` pulls `golang.org/x/term` and through it
**introduces** `golang.org/x/sys` — **banned in the SDK and its framework**.
Measured, not assumed: a probe module importing only `x/crypto/ssh` requires
`x/sys` indirect. It was `third-party/entitlement`, a package of the root module,
until ADR 0158; the quarantine is the one ADR 0022/0034 (the HCL codec) and ADR
0012 (the AWS writers) drew, now one module wide.

Its production code imports `framework/entitlement` — the public package, the way
the database engines import `framework/kit` — plus `internal/kernel/errs` for its
one sentinel and `x/crypto/ssh`. Its suite also reaches the engine's unexported
surface (`framework/internal/service/entitlement`), which Go's `internal/` rule
admits because this module's path is under `framework/`.

Everything it says about VERIFICATION is said in `framework/internal/core/entitlement`'s
vocabulary, range `0.2.35.*`. ENROLMENT is not in that contract — minting a pair
is something this package does and the port does not describe — so it owns one
code of its own, `0.3.65.1` `ENROLMENT_FAILED`, in the range `codeRangeOwners`
has recorded for this package since ADR 0078.

## Contents

| File | Role |
|---|---|
| `sshidentity.go` | the package doc, and `SSHIdentity` — the three port methods over a key directory, plus the `BoundProver` sibling |
| `sshidentity_compliance.go` | the compile-time proof that it still satisfies the port |
| `discover.go` | which subject this machine is enrolled as, and the refusal to guess |
| `key.go` / `key_unix.go` / `key_windows.go` | load, fingerprint, prove possession, permission checks |
| `enroll.go` | `NewSubjectID`, `GenerateKeyPair`, `IssueURL` |
| `codes.go` / `errors.go` | `CodeEnrolmentFailed` and its sentinel |
| `go.mod` | the module: the SDK module `github.com/kitsunium/sdk` — the framework and `internal/kernel/errs` — and `golang.org/x/crypto` |
| `wrap.go` | `refuse` / `classify` / `classifyForeign` / `annotate` |

## What it is NOT

A wall against a determined attacker. A check running on someone else's machine
is removable by definition. It stops casual sharing and makes revocation real
for cooperative installs, and the package doc says so rather than implying
otherwise.

## Why-this-shape

- **Possession is proven against material the user ALREADY has.** A file this
  package invented would need a lifecycle — where it lives, who may read it, what
  happens on rotation — that a user's own key directory already has.
- **`DiscoverSubject` refuses rather than guesses.** Several identities in one
  directory is ambiguous, and picking one would silently decide which licence
  gets verified, which one a rotation overwrites, and which one `status`
  reports. It sorts only so the diagnostic is stable, never to choose.
- **A stray `.pub` must not win.** `usableIdentities` narrows to the subjects
  whose private half is present as a real FILE — a directory or a FIFO named
  `<uuid>.pub` stats without error and would otherwise count as an identity.
- **`GenerateKeyPair` validates the subject BEFORE building any path.** It is
  the only entry point here that WRITES, and a subject like
  `../authorized_keys` wrote both halves outside the caller's directory until
  a review caught it. `TestGenerateKeyPairRefusesAPathTraversal` pins it.
- **The public sentence names no particular, and that is the point.** Every
  refusal is built with `refuse` or `classify`, so `err.Error()` is the
  wire-safe half and nothing else: no key directory, no subject, no path, no
  mode. Where it happened travels in `Fields`.
  `TestNoParticularReachesThePublicSentence` asserts both directions on four
  refusals, because leaking a particular and losing it are both defects and
  only one of them is the one everybody remembers.

- **`ProvePossessionFor` re-reads the directory, and that is the whole of what
  it adds.** The engine's comparison happens between its `Fingerprint` call and
  its proof call, so material replaced in that window was signed for and nothing
  noticed. Comparing the published half against the value the roster authorised
  HERE closes it, because the comparison and the signature then happen against
  one read. It is not a second fingerprint POLICY: the same rendering, compared
  by byte equality to the roster's own spelling, exactly as `Fingerprint`'s
  contract already requires. The refusal is `entitlement.ErrKeyMismatch` — the
  taxonomy does not grow — and it names the subject in FIELDS and neither the
  subject nor the authorised value in the wire-safe sentence, because this
  refusal is reachable by anyone who can write the key directory. `answer` is
  shared with `ProvePossession` so the challenge cannot drift between the two.
  ADR 0092.

- **`ProvePossession` takes the CALLER's signer, so origin-wins is wrong
  there.** An `ssh.Signer` or an `ssh.PublicKey` a caller supplies is free to
  return an `*errs.Error` of its own, and `errs.Wrap` would make it the identity
  of a possession failure. `classifyForeign` hides it from origin-wins and
  leaves it matchable by `errors.Is`.

- **`DiscoverSubject` drops the read error's CHAIN, not its text.** An
  unreadable key directory and a never-enrolled one are documented as ONE
  answer; keeping `os.ReadDir`'s failure as a cause would make
  `errors.Is(err, fs.ErrNotExist)` newly answer true there — a branch no caller
  has today. The text travels as a field instead.

- **The published half is world-readable on purpose.** The roster hands it to
  everyone, so protecting it locally would be theatre. The private half is
  `0600` and `SignerFromFile` refuses anything looser.

## Do NOT

- Build an error with `fmt.Errorf`. There were 25 of them and there are none;
  the three shapes in `wrap.go` are what a call site chooses between.
- Reintroduce an origin, a cache directory, an audience or an enrolment URL as a
  package constant. That is `entitlement.Product`'s job, and the suite injects a
  product naming a vendor the implementation never mentioned so a reintroduced
  constant fails rather than passes.
- Add the verification mechanism back here. It is `framework/internal/service/entitlement`
  now, and the whole point of ADR 0079 is that a consumer can reach it without
  this module's dependency graph.
- Drop the `var _ entitlement.BoundProver = (*SSHIdentity)(nil)` assertion. The
  engine reaches the sibling by TYPE ASSERTION, which cannot fail a build, so an
  implementation that silently stopped satisfying it would fall back to the
  unbound proof and nothing would say so. That line is what makes it a compile
  error instead.
- Move this into the SDK module (the framework's packages are its own since ADR
  0162), or anywhere a module without ssh would require it. The measurement above is why, and it is reproducible in one
  `go mod tidy`.
- Import the engine's internal packages from production code. The public
  `framework/entitlement` package is the contract this connector implements;
  only the suite reaches below it.

## Verification

```sh
bazel test --config=race //framework/connectors/ssh:ssh_test
(cd framework/connectors/ssh && GOWORK=off go test -race ./...)   # also run by make test-framework
```
