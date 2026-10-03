<!-- updated: 2026-10-03T03:30:00Z -->
# framework/selfupdate/

## Purpose

Thin **public facade** over `framework/internal/service/selfupdate` (ADR 0077): replace the
running binary with a newer signed release. Consumers import this package; the
service and `core/selfupdate` stay internal.

It was `pkg/v1/selfupdate` until ADR 0158 made the distribution mechanisms the
framework's: the surface is unchanged, the import path is
`github.com/kitsunium/sdk/framework/selfupdate`, and a consumer required the
framework module rather than `pkg` — one module, the SDK's, since ADR 0162.
Every code keeps its value (ADR 0160).

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Getter` / `FileSystem` / `Copier` | type alias | `= coreupd.*` — the three ports |
| `Update` / `Candidate` | type alias | the reported values |
| `Source` | type alias | `= svcupd.SourceValue` — one engine's parameters (ADR 0074) |
| `Service` | type alias | `= svcupd.Service` — the engine handle (ADR 0074) |
| `New` / `NewWithDeps` | func | delegate verbatim |
| `StdinIsTerminal()` | func | whether a human could answer a prompt on stdin — the `interactive` argument `Source.AuthoriseUnattendedUpgrade` takes |
| `CandidateListSentinel` | const | the tag value meaning "list the candidates", which a CLI flag taking an optional value must spell exactly |
| `Code*` | const | all 26 codes, for `errs.HasCode` — the contract's eighteen (`0.2.34.*`) and the implementation's eight (`0.3.66.*`, `CodeProbeFailed` the eighth — ADR 0150) |
| `NoVendorKey` … `ProbeFailed` | var | the 26 sentinels, one per code, for `errors.Is`; each keeps its `*errs.Error` |

The codes are re-exported deliberately, following `pkg/v1/security/authz`: a consumer of
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
nothing. Chain `WithVendorKey` with the build's linked-in anchor, or
`WithVendorKeys` with an ordered list of at most four when the key rotates
(ADR 0150).

## README is generated

`README.md` comes from `gomarkdoc` (ADR 0008). Regenerate with
`make docs-readme` — which runs `cd framework && go generate ./...`, not `gomarkdoc`
from the repository root. Do **not** hand-edit it.

## Verification

```sh
bazel test --config=race //framework/selfupdate:selfupdate_test
(cd framework && GOWORK=off go test -race ./selfupdate/...)
```
