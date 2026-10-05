# pkg/v1/proc/ipc/

## Purpose

Public facade over `internal/service/proc/ipc` (ADR 0148): a private socket between
processes of one machine, with the kernel's word on the peer where it gives
one. Its contract — the two ports, the values and the codes — is
`internal/core/proc/ipc` (ADR 0160), and the aliases point there (ADR 0074).

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Listener`, `Dialer` | type alias, port | `internal/core/proc/ipc`'s two ports — frozen, so a double written against them keeps compiling (ADR 0039) |
| `Peer`, `Conn` | type alias, value | `internal/core/proc/ipc`'s `PeerValue` and `Conn` |
| `Config` | type alias | the engine's configuration, `internal/service/proc/ipc.Config` (ADR 0074 — one engine's configuration is the engine's) |
| `Listen` | func | returns the socket engine behind the `Listener` port, or a nil port with the refusal |
| `NewDialer` | func | returns the engine behind the `Dialer` port; the configuration is checked and copied once |
| `Dial`, `RuntimeDir` | func | delegate verbatim |
| `Code*` | const | the nine codes, for `errs.HasCode`, aliasing the core |

## The ports, and what changed for a caller

`Listener` was the engine's own handle until ADR 0160 gave `ipc` a core; it is
now the core port, exactly as `systemd/notify`'s `Listener` is, and `Listen`
returns the engine behind it. Every method the handle had is on the port —
`Accept`, `Addr`, `Close`, `Path`, `Refused` — so a caller that only called
them compiles unchanged; one that spelled the type `*ipc.Listener` writes
`ipc.Listener` (a v0 shape change, ADR 0040). The framework's two holders
(`framework/internal/kit`, `framework/telemetry`) moved in the same change.

Both constructors return a NIL port with the refusal, never a nil engine
wrapped in a non-nil interface — the trap `if ln != nil` would fall into.

## Why-this-shape

The codes are re-exported because the caller's next move depends on them:
`IN_USE` means another daemon runs (talk to it), `DIAL_FAILED` means nobody
answers (start one), `DIRECTORY_UNSAFE` and `ENDPOINT_FOREIGN` mean the
deployment is wrong (never retry), and `PATH_UNSAFE` means a component ABOVE
the socket's directory is one another account could have planted or created,
or can replace — a deployment to fix or an attack to look at, never a retry
(the rule is `internal/service/proc/ipc/chain_unix.go`'s).

## README is generated

`README.md` comes from `gomarkdoc` (ADR 0008): `make docs-readme`.

## Generated

`facade_gen.go` is kit's (ADR 0165): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/proc/ipc.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test //pkg/v1/proc/ipc:ipc_test
```
