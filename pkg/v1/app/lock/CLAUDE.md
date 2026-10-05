<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/app/lock/

## Purpose

Public facade for the SDK's mutual exclusion (ADR 0052). Re-exports the port,
the lease, the `Deadliner` sibling and the configurations as **type aliases**
(zero runtime cost) and the sentinels as variables, plus the two constructors
and the keepalive helper, so consumers depend only on `pkg/v1`.

## Surface

| Symbol | Notes |
|---|---|
| `Locker` / `Lease` / `Deadliner` | aliases onto `internal/core/app/lock`. `Locker` FROZEN at two methods, `Lease` at three (ADR 0039) |
| `MemoryConfig` / `FileConfig` / `KeepaliveConfig` | aliases onto `internal/service/app/lock` |
| `NewMemory(cfg)` | in-process; leases **expire**; implements `Deadliner` |
| `NewFileLocker(cfg)` | one machine, via `flock(2)` on Unix and `LockFileEx` on Windows (ADR 0081); leases **never expire**; does NOT implement `Deadliner`; a symbolic link or reparse point at the lock path is REFUSED, never followed (ADR 0082), and so is one at a PARENT component where anybody could have planted it (ADR 0083). "Anybody" is a mode bit on Unix and the directory's DACL on Windows, where "may add an entry" without "may delete a child" is the sticky shape and is accepted (ADR 0084, ADR 0086) |
| `Keepalive(ctx, lease, cfg)` | background renewal → a context cancelled when the lease is lost |
| `LockMisconfigured` / `LockNotHeld` / `LockBackendFailed` / `LockNameRejected` | core sentinels |
| `LockFenceCorrupt` / `LockDirectoryUnsafe` / `LockKeepaliveLost` / `LockPathRedirected` | the lockers' sentinels (`0.3.51.*`), declared in `core/app/lock` |
| `LockFileReplaced` | the lockers' fifth sentinel, and the only one this domain DETECTS rather than prevents: the file a lease holds was unlinked or replaced while held, so the exclusion is gone and the holder is told (ADR 0083) |

## Conventions

- **Type aliases, not new types** — `pkg/v1/app/lock.Lease` IS `core/app/lock.Lease`.
- **A zero TTL is refused, never defaulted** (ADR 0031). Its two natural
  readings are opposites, and one of them grants every `Acquire` while
  excluding nobody.
- **`Deadliner` is the question that changes how a caller is written.** Present
  → this lease can be taken from you while you run; absent → it cannot.
- README written by `tools/genindex` from `docs/api` (ADR 0167); the package
  comment is in `doc.go`, which kit writes from `design/app/lock.yaml`: edit the
  design, run `kit gen`, then `make api` and `make docs-readme`.

## Do NOT

- **Weaken the "not guaranteed" paragraph in the package doc comment.** The SDK
  issues a fencing token; only the protected resource can enforce it. Without
  that check, mutual exclusion is not guaranteed against a stalled holder. It
  is stated in four places on purpose.
- **Add a distributed backend here.** Redis / etcd / ZooKeeper / Consul are
  connectors to third-party systems and belong under `third-party/`.
- **Soften `LockPathRedirected` into a warning, or offer a flag to follow the
  link anyway.** It would be a flag to re-enable the defect, and the deployment
  that would reach for it is the one that most needs to know (ADR 0082).
- **Describe `LockFileReplaced` as preventing a split.** It does not. A lock
  file can be unlinked out from under its holder by an account the directory
  permits, the second holder really does acquire, and what ships is that the
  first one finds out at its next `Extend`. Saying otherwise is worse than
  saying nothing, because a caller who believes the exclusion held behaves
  differently from one who knows it did not (ADR 0083 §D5).
- **Add a method to any aliased interface** — it breaks every downstream
  implementer at compile time with no deprecation window (ADR 0039).

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/app/lock.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/app/lock:lock_test
# or: cd pkg && GOWORK=off go test -race ./v1/app/lock/...
```
