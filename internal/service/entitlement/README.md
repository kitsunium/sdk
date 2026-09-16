# entitlement (internal/service/entitlement)

Decides whether this machine is entitled to run this build, against a
vendor-signed roster, and keeps deciding when the network is gone. Internal
service implementation behind the public `pkg/v1/entitlement` facade — consumers
import the facade, not this package.

## API

```go
func NewService(identity coreent.Identity, vendor []byte, product *ProductValue) *Service
func NewServiceWithGetter(client Getter, identity coreent.Identity, vendor []byte, product *ProductValue) *Service
func NewServiceWithOrigins(client Getter, identity coreent.Identity, vendor []byte, origins []coreent.OriginValue) *Service

func (s *Service) WithCache(dir string) *Service
func (s *Service) WithOrigins(origins []coreent.OriginValue) *Service
func (s *Service) WithVersion(version string) *Service
func (s *Service) WithBearerFetch(fetch BearerFetch) *Service
func (s *Service) WithTimeServers(servers []RoughtimeServerValue) *Service
func (s *Service) Verify(now time.Time) (coreent.GrantValue, error)

func ParseRoster(raw, sig []byte, vendor ed25519.PublicKey, now time.Time) (*coreent.RosterValue, error)
func ParseBundle(raw []byte, vendor ed25519.PublicKey, now time.Time) (*coreent.RosterValue, error)

func InCI() bool
func RequestActionsToken(get BearerFetch, audience string) (string, error)
func VerifyActionsToken(raw string, keys map[string]*rsa.PublicKey, audience string, now time.Time) (*ActionsClaimsValue, error)
func VerifyCI(get BearerFetch, keys map[string]*rsa.PublicKey, roster *coreent.RosterValue, audience string, now time.Time) (*ActionsClaimsValue, error)
func ParseJWKS(raw []byte) (map[string]*rsa.PublicKey, error)

func RequiresUpdate(current, floor string) bool
func UpdateRefusal(current, floor string) error
func QueryRoughtime(server RoughtimeServerValue) (time.Time, time.Duration, error)

func (p *ProductValue) Label() string
func (p *ProductValue) DefaultCacheDir() string
func (p *ProductValue) Validate() error
```

## The verification flow

This is the order `Verify` actually runs in. It used to be listed with the clock
at step 4 and the version floor absent entirely, which put the one check that
runs *before anything reads a date* after three that read dates.

1. `Discover` the subject. Outside CI an absent key refuses here, before any
   round trip; inside Actions the failure is DEFERRED, because a runner is not
   expected to hold a key and the seat is allowed to answer first.
2. **Doubt the clock first:** the anti-rollback ratchet, against the newest
   vendor-signed instant this machine ever authenticated, plus whatever network
   time corroboration is configured. Every deadline below is compared against a
   clock the holder owns, so this cannot run after them.
3. Fetch the roster from each origin in turn. Per origin: **authenticate before
   reading** — the detached ed25519 signature over the raw bytes, against the
   ORDERED LIST of vendor keys pinned at build time — the first that verifies
   wins, see anchors.go — so a roster that verifies against none of them is
   never decoded — then check its own window (`RosterLifetime`, 24 h), then check it is
   not a replay of something already superseded. The first origin to clear all
   three wins; any that fails is treated like an unreachable one and the loop
   moves on. When none clears them, the last authenticated bundle is replayed
   from the cache (see Offline).
4. Apply the roster's mandatory-update floor. BEFORE the subject lookup on
   purpose: an out-of-date binary must be told to upgrade whether or not its
   licence is also in order.
5. Try the CI seat: mint a GitHub Actions OIDC token, verify it against the
   issuer's published RSA keys, then look the account up in the roster. It costs
   no device seat.
6. Otherwise match the identity: compare the local `Fingerprint` against what the
   roster publishes, refuse a subject whose own term has closed, then require
   `ProvePossession`.
7. Return a `GrantValue` bounded by the earliest of the verification's lifetime
   and every document it rested on — the roster's expiry, the subject's term,
   and for a CI seat the Actions token's own expiry.

Step 3's internal order is load-bearing and the suite asserts it: a digest
checked against an unauthenticated document proves nothing.

### Anti-replay, and what it is conditional on

A roster the vendor signed but has since replaced cannot undo a decision this
machine already reached. The comparison happens at step 3, against the cached
bundle's `IssuedAt`, in the same locked section that installs the replacement —
so a genuine older roster from a lagging mirror or a substituted origin cannot
restore a revoked subject, an old fingerprint, a lower version floor, a withdrawn
CI account or a relaxed CI policy. Equal instants are accepted when the signed
payload is identical, which is every ordinary re-verification, and refused when
it is not.

It is CONDITIONAL on the mark persisting: it lapses with no cache configured,
with an install that failed, and with a lock that could not be taken. None of the
three refuses instead, because a contended lock turned into a refused licence
would be worse than the replay.

### The limit of the origin fallback

The loop moves on from a document it cannot USE, never from a document it can use
and does not like. So the first origin serving a roster that verifies, is in
window and is not a replay ends the search — including a roster entitling nobody.
A publisher shipping an empty `subjects` map to one mirror revokes every machine
reading it, and a healthy second mirror is not consulted. Continuing until an
origin authorises would be the worse defect: it would let any endpoint veto a
revocation. Reading an empty roster as a publication fault instead is a statement
only the vendor can make, and would have to travel in the signed document.

## Offline

When no origin answers, `cachedRoster` replays the last bundle this machine
authenticated and re-runs every check except the ones that need the network. The
grant is marked `Offline` so the fact can be said — a binary quietly running on a
document it can no longer fetch is the one state where "it worked" and "it is
still true" come apart.

The cache path is the ONE path the anti-replay comparison does not apply to, by
choice: the mark IS that file, so comparing what comes back against it is
tautological, and the only way it could disagree is a concurrent refresh — where
refusing would turn lock contention into a refused licence.

When nothing is cached either, the refusal is `RosterUnreachable` and it names
the NETWORK. It is not a refusal; a caller that treats it as one turns an outage
into a revocation.

## The CI seat is bounded by its own token

A CI grant's `NotAfter` includes the Actions token's `exp`, so a seat cannot
outlive the proof that established it. That is a CHANGE IN OBSERVABLE BEHAVIOUR:
the bound was the roster's window, up to 24 h, and it is now at most the token's
own window — 30 min (`maxTokenLifetime`) in the widest case GitHub mints, and in
practice the few minutes a real Actions token carries. A long-running process on
a runner that aged against a CI grant for hours now has to re-verify inside that
window.

No clock skew is added to the bound. `clockSkew` (2 min) exists to ADMIT a token
whose clock disagrees slightly, which is the permissive direction and the right
one there; the same allowance on a bound would extend the grant past the proof.
The two together are what makes the exposure of a leaked token statable: one free
seat PER VERIFIER for up to 32 minutes — a short token bounds the duration a
stolen proof works for, never the number of processes that can present it.

## Errors

Fourteen sentinels, all `errs.Define`-typed in `internal/core/entitlement`,
range `0.2.35.*`. `errors.Is` and `errs.HasCode` both work through the
wrapping. A fifteenth code, `CodeProductInvalid`, has no sentinel: `Validate`
mints one error per failure with `errs.Wrap`, because the caller needs to know
WHICH assertion the product failed.

| Class | Sentinels | What a caller should do |
|---|---|---|
| cannot decide | `ErrRosterUnreachable` | keep serving, tell the operator |
| refused | `ErrRevoked`, `ErrLicenseExpired`, `ErrKeyMismatch`, `ErrNoPossession` | stop |
| not enrolled | `ErrNoLicense`, `ErrAmbiguousLicense` | enrol, or disambiguate |
| CI | `ErrCIUnverifiable`, `ErrCIUnknownKey`, `ErrCINotEntitled` | fall back to a device seat |
| tamper | `ErrRosterUnsigned`, `ErrClockRegressed` | do not retry |
| other | `ErrRosterStale`, `ErrUpdateRequired` | refresh, or upgrade |
| construction | `CodeProductInvalid` (no sentinel) | fix the product's origin list |

## Identity

`NewService` takes a `coreent.Identity` — three methods, none of which says
"ssh". The ssh implementation is `third-party/entitlement.NewSSHIdentity(dir)`
and lives outside this module because `golang.org/x/crypto/ssh` reaches
`golang.org/x/sys` (ADR 0079).
