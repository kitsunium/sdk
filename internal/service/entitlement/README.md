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

1. Fetch the roster from each origin in turn; the first that authenticates wins.
2. **Authenticate before reading:** the detached ed25519 signature over the raw
   bytes, against the vendor key pinned at build time. A roster that does not
   verify is never decoded.
3. Check the roster's own window (`RosterLifetime`, 24 h).
4. Check the clock against the highest generation this machine has ever seen —
   the anti-rollback ratchet.
5. Try the CI seat: mint a GitHub Actions OIDC token, verify it against the
   issuer's published RSA keys, then look the account up in the roster. A seat
   costs no device seat.
6. Otherwise match the identity: `Discover` the subject, compare its
   `Fingerprint` against what the roster publishes, then require
   `ProvePossession`.
7. Return a `GrantValue` bounded by the earliest of the verification's lifetime,
   the roster's expiry and the subject's term.

Step 2's order is load-bearing and the suite asserts it: a digest checked
against an unauthenticated document proves nothing.

## Offline

When no origin answers, `cachedRoster` replays the last bundle this machine
authenticated and re-runs every check except the ones that need the network. The
grant is marked `Offline` so the fact can be said — a binary quietly running on a
document it can no longer fetch is the one state where "it worked" and "it is
still true" come apart.

When nothing is cached either, the refusal is `RosterUnreachable` and it names
the NETWORK. It is not a refusal; a caller that treats it as one turns an outage
into a revocation.

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
