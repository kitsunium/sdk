# pkg/v1/selfupdate/

## Purpose

Thin **public facade** over `internal/service/selfupdate` (ADR 0077): replace the
running binary with a newer signed release. Consumers import this package; the
service and `core/selfupdate` stay internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Getter` / `FileSystem` / `Copier` | type alias | `= coreupd.*` — the three ports |
| `Update` / `Candidate` | type alias | the reported values |
| `Source` | type alias | `= svcupd.SourceValue` — one engine's parameters (ADR 0074) |
| `Service` | type alias | `= svcupd.Service` — the engine handle (ADR 0074) |
| `New` / `NewWithDeps` | func | delegate verbatim |
| `Code*` | const | the eighteen codes, for `errs.HasCode` |

The codes are re-exported deliberately, following `pkg/v1/authz`: a consumer of
THIS domain must distinguish a transient failure from a supply-chain refusal, and
a facade that hid the codes would force it to match on message text.

## Why the codes matter more here than elsewhere

Three classes, and conflating them is a real hazard:

| Class | Codes | What a caller should do |
|---|---|---|
| transient | `CodeDownloadFailed`, `CodeUnexpectedStatus` | retry |
| recoverable by opt-in | `CodeElevationNotAuthorised` | tell the operator which variable to set |
| supply chain | `CodeSignatureMissing`, `CodeSignatureInvalid`, `CodeChecksumMismatch` | **do not retry, and do not install by hand** |

The last row is why this package exists in this shape. A missing or wrong
signature is exactly what a substituted release looks like, and a checksum
cannot tell the two apart.

## No key, no install

`New` returns a Service carrying no vendor key, and such a Service installs
nothing. Chain `WithVendorKey` with the build's linked-in anchor.

## README is generated

`README.md` comes from `gomarkdoc` (ADR 0008). Regenerate with
`make docs-readme` — which runs `cd pkg/v1 && go generate ./...`, not `gomarkdoc`
from the repository root. Do **not** hand-edit it.

## Verification

```sh
bazel test --config=race //pkg/v1/selfupdate:selfupdate_test
```
