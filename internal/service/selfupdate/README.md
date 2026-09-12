# selfupdate (internal/service/selfupdate)

Replaces the running binary with a newer signed release. Internal service
implementation behind the public `pkg/v1/selfupdate` facade — consumers import
the facade, not this package.

## API

```go
func NewService(version string, src SourceValue) *Service
func NewUpdaterWithDeps(version string, src SourceValue, client coreupd.Getter,
                        fs coreupd.FileSystem, copier coreupd.Copier) *Service

func (u *Service) WithVendorKey(key []byte) *Service   // required
func (u *Service) CheckForUpdate() (UpdateValue, error)
func (u *Service) Upgrade() (UpdateValue, error)
func (u *Service) ListCandidates() ([]CandidateValue, error)
func (u *Service) DownloadCandidate(tag string) (UpdateValue, error)

func (s SourceValue) AuthoriseUnattendedUpgrade(out io.Writer, in io.Reader, interactive bool) bool
func (s SourceValue) ExplainUpgradeRefusal(out io.Writer)
func (s SourceValue) ExplainUpgradeFailure(out io.Writer, action string, err error)
func (s SourceValue) AutoUpgradeEnv() string
func (s SourceValue) SudoOptInEnv() string
```

## The update flow

1. Fetch the latest release from `StableRepo`.
2. Compare with the running version (`semver.Compare`).
3. Download the platform archive into a size-capped buffer.
4. **Authenticate, in this order, before anything touches the disk:**
   1. the detached ed25519 signature over `checksums.txt`, against the vendor key;
   2. the archive's SHA-256 against the now-trusted manifest entry.
5. Extract the inner binary from the verified bytes.
6. Replace atomically: temp file → `chmod 0755` → `os.Rename`.

Step 4's order is load-bearing and the suite asserts it.

## Errors

Eighteen typed sentinels in `core/selfupdate`, in three classes:

| Class | Codes |
|---|---|
| transient — retry | `DownloadFailed`, `UnexpectedStatus` |
| recoverable by opt-in | `ElevationNotAuthorised` |
| supply chain — do not retry, do not install by hand | `SignatureMissing`, `SignatureInvalid`, `ChecksumMismatch`, `ChecksumMissing`, `NoVendorKey` |

## Configuration

Everything product-specific lives in `SourceValue`: the release host, the two
repositories, and the product name — which is both the asset stem
(`<product>_<goos>_<goarch>.tar.gz`) and, uppercased with punctuation folded, the
prefix of the two opt-in environment variables.

## See also

- `pkg/v1/selfupdate` — the public facade
- `internal/core/selfupdate` — the ports, values and sentinels
- ADR 0077 — the decision and its limits
