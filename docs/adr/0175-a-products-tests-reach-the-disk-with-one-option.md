# ADR 0175 — a product's tests reach the disk with one option, and the last of InMemory and DataDir wins

- **Status**: Proposed. This is the port of a change that was already written and verified on kitsunium/platform. It waits for the owner's review.
- **Date**: 2026-10-14
- **Deciders**: SDK maintainers
- **Related**: [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the framework is a module of the SDK), [ADR 0056](0056-sdk-vfs-domain.md) (the disk file system, refused on Windows), kitsunium/platform#35 item 6 (`d1e5961`), kitsunium/sdk#274

## Context

A product's test suite runs the product in memory (`kit.InMemory()`). That
is fast and hermetic, but it never touches what kit does on disk:

- kit seals nothing in memory;
- a transaction in memory rolls back in memory, not in files;
- a store's retention never folds anything into a snapshot;
- a restart reads back nothing.

Vigie hit a race that showed up only on disk (platform#35, point 3). The
platform fixed this in `d1e5961`, a `kit/kittest` package, which never
reached the SDK. kit's engine moved here (ADR 0147) before that branch
could be merged, so `framework/` has no `kittest` today.

The platform commit has one prerequisite. Its helper adds a data
directory to a suite helper that already gave `kit.InMemory()`. In the
SDK, `InMemory` always wins, whatever the order, so the option the test
adds would be silently ignored.

## Decision

### D1 — `kittest.NewDisk(t)` returns a `*Disk`

A `Disk` has three parts:

- a data directory of the test's own (`t.TempDir()`), removed when the
  test ends;
- the secrets kit generates, kept beside the data, as production keeps
  them unless `KIT_SECRETS` says otherwise. A product started again on the
  same `Disk` therefore opens what the first run sealed;
- an embedded `kit.AppOption` (`kit.DataDir(dir)`). A suite helper that
  takes options runs on disk with the one option the test adds.

`NewDisk` probes the SDK's disk file system (`vfs.NewOS`). On a platform
where that file system answers `UnsupportedPlatform` (Windows today),
`NewDisk` skips the test. Every other failure fails the test.

### D2 — `Disk.Holding(t, words...)` names the files that keep a word in clear

`Holding` returns every file under the data directory whose bytes contain
one of the words:

- the paths are relative, slash-separated and sorted;
- kit's `.secrets` directory is skipped, because its whole content is
  secrets;
- an empty word matches nothing.

This is the assertion a sealing test needs: what kit should have sealed
is not found in clear on disk.

### D3 — the last of `InMemory` and `DataDir` wins

`kit.DataDir` given after `kit.InMemory()` now places the data in that
directory. `kit.InMemory()` given after `kit.DataDir` still keeps
everything in memory, as before. On a store, `InMemory` is unchanged and
keeps that store in memory. Option order already decides for every other
app option.

## Consequences

- A product's suite tests what rests on disk with one more option.
  framework/kit/kittest's own tests show it: personal fields sealed, a
  public field in clear, and a restart on the same `Disk` that reads
  everything back.
- **Behaviour change, named**: an app given `InMemory()` and then
  `DataDir(d)` used to run in memory and now runs on `d`. No product in
  the platform's history gave the two in that order meaning memory, as
  the platform recorded in `d1e5961`. The module is v0.
- `framework/kit/kittest` is a new public package. The release is a minor.

## Not done here

- kit's own internal tests do not move to `kittest`. They live in
  `framework/internal/kit`, which `kittest` imports through the facade, so
  they cannot import it back. They keep `kit.DataDir(t.TempDir())`.

## Verification

```sh
cd framework && go test -race ./kit/kittest/ && go test -run TestTheLastOfInMemoryAndDataDirWins ./internal/kit/
```
