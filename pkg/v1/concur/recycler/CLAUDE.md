<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/concur/recycler/

## Purpose

Public facade for `internal/kernel/concur/recycler` (ADR 0159 §4, ADR 0010):
typed object pools over `sync.Pool` — `Pool[T]`, and `CappedPool[T]`, which
resets a value on `Put` and drops, unreset, one whose capacity grew past a
threshold. Aliases and forwarding constructors only — no behaviour of its own.
Stdlib-only → dep-light; cross-OS portable.

It is the pool the SDK's logger, codecs, transforms and network server recycle
their buffers through, published so a program — and the framework, which
reaches the SDK through `pkg/v1` alone (ADR 0147) — pools on the same
mechanism instead of a type-asserting `sync.Pool` of its own.

## Surface

| Symbol | Notes |
|---|---|
| `Pool[T]` | alias; `Get` / `Put`. Resets nothing |
| `CappedPool[T]` | alias; `Get` / `Put` — reset before repooling, drop-before-reset past the cap |
| `NewPool[T](newFn)` | a nil factory panics here, not at the first empty `Get` |
| `NewCappedPool[T](newFn, resetFn, capOfFn, maxCap)` | every function required and `maxCap > 0`, or a panic here |

That is the complete exported surface of `internal/kernel/concur/recycler`.

## Conventions

- **Pool a pointer** — the package comment repeats the kernel's measurement
  (a `[]byte` header costs an allocation per `Put`; a `*[]byte` costs none),
  because it is the one mistake the type system lets through.
- **Reset is not a wipe**, and a dropped value is never reset: its backing
  bytes may already belong to someone else (the codec scratch detach
  contract). Both are stated to the consumer, not only in the kernel.
- **Methods are documented on the alias** and linked `[Pool].Get` (ADR 0138).
  Forwarding constructors, not `var`s (see `data/semver`).
- The package comment is in `doc.go`, which kit writes from the design
  (`design/kernel/concur.yaml`, ADR 0167): edit the design, run `kit gen`, then `make api`
  and `make docs-readme`. `README.md` is written by `tools/genindex` from
  `docs/api`; do not hand-edit it.

## Do NOT

- Add a byte-slice pool here. `internal/kernel/concur/buffer` is
  `CappedPool[*[]byte]` specialised, and stays unpublished on its own
  (ADR 0159 §4): a consumer builds the same with `NewCappedPool`.
- Reach for `internal/kernel/concur/recycler` in this package's tests.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/kernel/concur.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/concur/recycler:recycler_test
cd pkg && GOWORK=off go test -race ./v1/concur/recycler/
```

`recycler_external_test.go` is `package recycler_test`: `Get` builds through
the factory, `Put` resets a value within the cap and drops one past it
untouched, the four construction mistakes panic — plus `ExampleNewCappedPool`.
No assertion depends on `sync.Pool` handing a value back: under the race
detector it drops a share of `Put`s on purpose.
