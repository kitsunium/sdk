<!-- updated: 2026-10-14T00:00:00Z -->
# framework/app/apptest — a product's tests on disk

A product's suite runs in memory. The tests about what rests on disk run
here: sealing, transactions that write files, retention folding, and a
restart reading everything back (ADR 0175, ported from kitsunium/platform
`d1e5961`).

## Contents

| File | Holds |
|---|---|
| `apptest.go` | `NewDisk`: a data directory of the test's own, with the secrets kit keeps beside it, and the `app.AppOption` that runs an app there (`app.DataDir`, which wins over an earlier `app.InMemory()`); it skips where `vfs.NewOS` answers `UnsupportedPlatform`. `Disk.Holding`: the files that keep one of the words in clear, sorted, with `.secrets` skipped |
| `doc.go` | the package comment, kept by `design/framework/app.yaml` (`docs: design`) |
| `apptest_external_test.go` | a desk product run through the facade only: sealed on disk, a public field in clear, read back after a restart, and `Holding` in order |
| `gen_api_test.go` | the pins kit writes from the design |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`) |

## Rules

- This package uses the facade (`framework/app`) only, so it sees an app as a product does. kit's own internal tests cannot import it, because it imports their package through the facade.
- A test that runs an app imports `framework/app/server`, because the default profile serves HTTP, and the registration package of each subsystem its app declares — `framework/app/store` for this package's own suite, whose desk keeps notes: since ADR 0179's step 10 the facade imports none of them, and the start refuses a declaration whose package is not imported (`CodeSubsystemMissing`).

## Verify

```sh
cd framework && go test -race ./app/apptest/
```
