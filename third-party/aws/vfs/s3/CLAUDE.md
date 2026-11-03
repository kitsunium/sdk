<!-- updated: 2026-11-02T22:00:00Z -->
# third-party/aws/vfs/s3/

## Purpose

A `core/data/vfs` `FullFS` over one S3 bucket, or one key prefix of it
(ADR 0056's port, ADR 0211, kitsunium/sdk#991). It lives in the
`third-party/aws` module beside the s3 and cloudwatch writers, on the vendor
the module already carries (ADR 0157): a consumer of it gets the AWS SDK and
`internal/*`, no other vendor. The docstore's file engine runs over it
unchanged (ADR 0110).

## Surface (design: `design/third-party/aws.yaml`)

`New(Config) (vfs.FullFS, error)`; `Config` (`Bucket`, `Prefix`, `Region`,
`Endpoint`, `Credentials`, `MaxKeys`, `Timeout`); `MaxObjectBytes` (5 GiB, one
`PutObject`); `ModeMetadata` (`"mode"`, the user-metadata key a mode is
recorded under); codes `0.3.98.*` — `S3FSMisconfigured`, `BucketMissing`,
`AccessDenied`, `Throttled`, `ObjectTooLarge`, `RequestFailed`.

## The mapping

| Verb | S3 |
|---|---|
| `Open`, `ReadFile` | `GetObject`; a missing key falls back to the directory lookup |
| `Stat` | `HeadObject` of the key, then of the directory marker, then a one-key `ListObjectsV2` |
| `ReadDir` | `ListObjectsV2` with the `/` delimiter, paged by `MaxKeys`, sorted; a dir entry's `Info` is a `Stat` |
| `WriteFile` | one `PutObject`; a rewrite keeps the mode the object had (`HeadObject` first) |
| `WriteAtomic` | one `PutObject`, atomic per key; the mode applies on every publication (ADR 0056, 2026-11-02) |
| `MkdirAll` | a zero-byte marker `<dir>/` per missing directory, carrying the mode |
| `Remove` | `DeleteObject` of the file, or of an empty directory's marker |
| `RemoveAll` | `DeleteObject` of the name, then `DeleteObjects` page by page |
| `Lstat`, `ReadLink` | `fs.ReadLinkFS` as the memory engine answers it: no link exists |
| `SyncDir` | `core/data/vfs.DirSyncer` (asserted at compile time in `conformance_internal_test.go`): the memory engine's refusals, nothing to flush |

A directory exists when its marker does or when some key extends its prefix.
A missing parent is refused as on disk (`WRITE_FAILED` over `fs.ErrNotExist`),
a file on the path is `NOT_REGULAR_FILE`. Errors keep the vfs codes and add the
package's: `READ_FAILED` / `WRITE_FAILED` / `PUBLISH_FAILED` in the trail of
a vendor sentinel, `fs.ErrNotExist` kept for an absent key.

## Contents

| File | Role |
|---|---|
| `config.go` | `Config`, its refusals by field, `MaxObjectBytes`, `ModeMetadata` |
| `client.go` | `objectAPI`, the six S3 calls — the seam the tests double — and `newClient` |
| `cred_adapter.go` | `credAdapter`, `writer.CredentialProvider` onto `aws.CredentialsProvider` |
| `fs.go` | `s3FS`, `New`, keys, `Open`, `Stat`, `ReadFile`, the lookups, modes |
| `list.go` | `ReadDir` and the paged listing |
| `write.go` | `WriteFile`, `WriteAtomic`, `MkdirAll`, the parent checks, `put` |
| `remove.go` | `Remove`, `RemoveAll`, `SyncDir` |
| `file.go`, `dir.go`, `entry.go`, `info.go` | the `fs.File`, `fs.ReadDirFile`, `fs.DirEntry` and `fs.FileInfo` |
| `errors.go` | the S3 error classified onto the package's sentinels; the vfs wraps |
| `gen_codes.go`, `doc.go`, `gen_api_test.go` | written by kit gen from the design |

## Tests

- **Every lane** — `conformance_internal_test.go` restates the rows of
  `internal/service/data/vfs`'s conformance table (and `testing/fstest.TestFS`)
  and runs them over `fakebucket_internal_test.go`, an in-memory bucket that
  answers as S3 does — `NoSuchKey`, `NotFound`, a delimited and paged
  `ListObjectsV2`; `docstore_internal_test.go` runs the docstore's write,
  version and reopen cycle over it; `errors_internal_test.go` and
  `config_internal_test.go` the refusals and the error mapping.
- **LocalStack, opt-in** — `localstack_test.go`, `//go:build localstack`
  (rule 12): the same rows, the docstore cycle and a torn-read row (four
  readers during 1 000 republications see only whole versions) against the
  emulator. Run as the writers' suites:
  `docker run --rm -p 4566:4566 localstack/localstack`, then
  `cd third-party/aws && GOWORK=off go test -tags localstack -race ./vfs/s3/...`
  (`LOCALSTACK_ENDPOINT` overrides the endpoint). No lane runs it until
  kitsunium/sdk#988's scheduled vendor lane exists.

## Do NOT

- Fake a rename: the port has none, and `PutObject` already is the atomic
  publication.
- Read the mode from anything but `ModeMetadata`: S3 has no mode of its own.
- Import this package from `internal/`, `pkg/` or `framework/`.
