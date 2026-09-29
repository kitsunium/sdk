# pkg/v1/ipc/

## Purpose

Public facade over `internal/service/ipc` (ADR 0144): a private socket between
processes of one machine, with the kernel's word on the peer where it gives
one.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Config`, `Peer`, `Conn`, `Listener` | type alias | the engine's values (ADR 0074) |
| `Listen`, `Dial`, `RuntimeDir` | func | delegate verbatim |
| `Code*` | const | the eight codes, for `errs.HasCode` |

## Why-this-shape

The codes are re-exported because the caller's next move depends on them:
`IN_USE` means another daemon runs (talk to it), `DIAL_FAILED` means nobody
answers (start one), `DIRECTORY_UNSAFE` and `ENDPOINT_FOREIGN` mean the
deployment is wrong (never retry).

## README is generated

`README.md` comes from `gomarkdoc` (ADR 0008): `make docs-readme`.

## Verification

```sh
bazel test //pkg/v1/ipc:ipc_test
```
