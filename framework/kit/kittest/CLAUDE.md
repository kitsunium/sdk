<!-- updated: 2026-10-14T00:00:00Z -->
# framework/kit/kittest — a product's tests on disk

A product's suite runs in memory. The tests about what rests on disk run
here: sealing, transactions that write files, retention folding, and a
restart reading everything back (ADR 0175, ported from kitsunium/platform
`d1e5961`).

## Contents

| File | Holds |
|---|---|
| `kittest.go` | `NewDisk`: a data directory of the test's own, with the secrets kit keeps beside it, and the `kit.AppOption` that runs an app there (`kit.DataDir`, which wins over an earlier `kit.InMemory()`); it skips where `vfs.NewOS` answers `UnsupportedPlatform`. `Disk.Holding`: the files that keep one of the words in clear, sorted, with `.secrets` skipped |
| `doc.go` | the package comment, kept by `design/framework/kit.yaml` (`docs: design`) |
| `kittest_external_test.go` | a desk product run through the facade only: sealed on disk, a public field in clear, read back after a restart, and `Holding` in order |
| `api_gen_test.go` | the pins kit writes from the design |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`) |

## Rules

- This package uses the facade (`framework/kit`) only, so it sees an app as a product does. kit's own internal tests cannot import it, because it imports their package through the facade.
- A test that runs an app imports `framework/kit/server`, because the default profile serves HTTP, and the registration package of each subsystem its app declares — `framework/kit/store` for this package's own suite, whose desk keeps notes: since ADR 0179's step 10 the facade imports none of them, and the start refuses a declaration whose package is not imported (`CodeSubsystemMissing`).

## Verify

```sh
cd framework && go test -race ./kit/kittest/
```
