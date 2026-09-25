# ADR 0095 — Windows runs every test and gates, and each of its nineteen failures was answered on its own terms

- **Status**: Accepted
- **Date**: 2026-09-25
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0094](0094-a-test-compiles-where-its-package-does.md) — Decision 5's Windows inventory and §Deferred "Windows at runtime" are superseded: the Windows lane gates; [ADR 0087](0087-the-root-a-caller-named-is-a-spelling-it-did-not-choose.md) §1 — the canonical root is cleaned into the OS's form, and its single `EvalSymlinks` becomes two
- **Related**: [ADR 0094](0094-a-test-compiles-where-its-package-does.md) (the inventory this closes — its §Deferred "Windows at runtime", and Decision 5's Windows half), [ADR 0018](0018-sdk-cross-platform-portability.md) (the `UnsupportedPlatform` floor), [ADR 0084](0084-the-windows-lock-directory-has-an-answer-and-it-is-not-a-mode.md) / [ADR 0086](0086-creating-an-entry-is-not-replacing-one-and-windows-says-so-in-two-bits.md) (the DACL reader reused — and where the queue parts from ADR 0084 §D5), [ADR 0077](0077-a-self-update-is-an-order-of-operations-and-a-product-name-is-not-part-of-it.md) (the Windows replacement step), [ADR 0087](0087-the-root-a-caller-named-is-a-spelling-it-did-not-choose.md) (the root spelling)

## Context

ADR 0094 ran every package's tests on `windows-latest` for the first time, as an
inventory (`continue-on-error`) that would become a gate once clean. Its first
run, recorded in that ADR's §Deferred, passed 184 packages and failed 19:
`internal/service/{codec/multipart, logger/sink/file, metrics, net/server,
net/sse, queue, selfupdate, vcs/git, vfs, writer/journald, writer/rotfile}` and
`pkg/v1/{cgroup, git, logger, metrics, process, queue, server, signal}`. A later
run added a twentieth, `net/client`, that the first had passed by luck.

A failing test on a new platform is one of two things, and the difference
decides the fix. Either the code does something wrong there — a production
bug, fixed in the code, with a test that pins it on every lane that can reach
it — or the test asserted a Unix behaviour Windows does not have, in which case
the test must assert what Windows DOES promise: the documented floor, never a
skip that hides a bug. Each failure below was classified before it was touched.

## Decision

1. **The Windows whole-suite step gates.** `continue-on-error` is removed from
   `Unit tests — every package on windows` in `.github/workflows/e2e-cross.yml`;
   a Windows regression now fails the job like a macOS one.

2. **Production bugs, fixed in the code:**
   - **`codec/multipart`** decoded a filename through
     `(*multipart.Part).FileName`, i.e. the HOST's `filepath.Base`, which
     splits on `\` on Windows and not on Unix: the same body decoded
     differently per OS. The decoder reads `Content-Disposition` itself and
     keeps the last element after `/` OR `\` on every OS (RFC 7578 §4.2: the
     sender writes the path with its own separator).
   - **`net/server`**, three: the shard report said `degraded=false` with a
     reason when auto-sizing without `SO_REUSEPORT`; Windows FAILS a read of a
     datagram longer than the buffer with `WSAEMSGSIZE` where Unix truncates,
     so an oversized datagram was never counted; and `unixgram`/`unixpacket`
     failed as `LISTEN_FAILED` where Windows' AF_UNIX simply has no such
     socket — now `UNSUPPORTED_PLATFORM` before the OS is asked, with a
     Windows test measuring that the families really are absent.
   - **`vcs/git`** kept git's `/`-separated, 8.3-expanded `--show-toplevel`
     raw while every entry went through `filepath.Join`, so the rewrite to the
     caller's spelling never matched: a repository under a runner's `%TEMP%`
     (`RUNNER~1`) resolved, was neither degraded nor empty, and answered
     false for an edited file. The root is cleaned into the OS's form, and
     both sides are resolved before they are compared (ADR 0087's single
     `EvalSymlinks` becomes two, on the same non-ordinary path).
   - **`queue`** read POSIX mode bits to judge its directories; Windows
     synthesises `0777` for every writable one, so every directory was refused
     as `QUEUE_DIRECTORY_UNUSABLE`. The rules now read the DACL on Windows
     through the one reader the repository has — `internal/service/lock`,
     which exports it as `GrantsAnyone` rather than have a second copy — and a
     junction counts as the indirection a symlink is. The broker still does
     not RUN there: its publisher, `vfs`, refuses Windows by design, so a
     directory the rules accept meets `UNSUPPORTED_PLATFORM` — the platform's
     answer, where it used to be a false verdict on the caller's directory.
     A list the reader cannot read, or reads only in part, is REFUSED
     (`why=unverifiable`, the status in `observed`). The lock accepts that
     case (ADR 0084 §D5), because a wrong refusal there costs a locker on a
     safe directory; a queue directory wrongly accepted is a planted message
     a consumer acts on, and the queue refused every Windows directory before
     this rule, so the refusal takes away nothing that worked. The reader now
     names every no-verdict answer — a path that is not one and an entry the
     walk cannot fetch used to come back empty, indistinguishable from a
     verdict — so a caller can make that choice at all.
   - **`selfupdate`** documented Windows as unsupported for the replacement
     step (ADR 0077) and enforced nothing: it downloaded and authenticated the
     archive, failed to rename over the running executable, fell back to
     `sudo -n mv`, and advised the sudo opt-in. A Service built for Windows now
     refuses with `UNSUPPORTED_PLATFORM` after `canAuthenticate` and before
     anything is fetched.
   - **`writer/journald`** failed as `JOURNALD_OPEN_FAILED` — a journal that is
     down — where Windows has no AF_UNIX datagram socket at all: the default
     dialer is refused with `UNSUPPORTED_PLATFORM` there.
   - **`logger`**: `NewMulti`, `DefaultMulti` and `FromConfig` open their
     writers on the caller's behalf and returned a Logger with no `Close`, so
     the files lived until a collection ran their finalizers — and on Windows
     could be neither deleted nor rotated. The Logger they return now owns
     the writers and is an `io.Closer` (`svclogger.Owning`); derived Loggers
     own nothing — `WithGroup("")` included, whose no-op would otherwise hand
     the owner itself back as its child.

3. **Unix premises in tests, replaced by what Windows promises:**
   - **Clock resolution** (`metrics`, `pkg/v1/metrics`, `net/sse`,
     `net/server`, `net/client`): Windows' clock moves in ticks of 0.5 to
     15.6 ms, so two readings microseconds apart are equal. No production code
     assumed otherwise. The tests inject a manual clock where the code takes
     one, wait until the clock passes the previous instant where it does not
     (`untilTheClockPasses`), and bound a measured duration below by a known
     latency rather than by zero.
   - **Modes** (`logger/sink/file`, `writer/rotfile`): `0600` reaches a file on
     Windows only as "not read-only" (`0666`); that bit is what is asserted,
     and both CLAUDE.md files state that the file inherits its directory's ACL.
     `chmod`-frozen directories cannot inject a failure under ACLs: those
     cases skip there, naming why.
   - **Open handles** (`logger/sink/file`, `writer/rotfile`, `selfupdate`,
     `pkg/v1/logger`): Windows will not delete or rename an open file. Tests
     close what they open, in `t.Cleanup`.
   - **Platform contracts asserted, then skipped** (`vfs`, `queue`,
     `pkg/v1/queue`, `net/server` adoption, `writer/journald`): where the
     fixture cannot exist — a disk filesystem vfs refuses, a child holding an
     inherited socket, a unixgram server — the test first ASSERTS the
     documented refusal and only then skips.
   - **Stale facade suites** (`pkg/v1/{cgroup, process, signal}`): the
     services gained Windows backends (a Job Object, CreateProcess,
     TerminateProcess) and moved their "unsupported" tests to
     `!unix && !windows`; the facades had kept a bare `!unix`/`!linux`. Their
     tags now mirror the services', a `*_windows_test.go` per facade asserts the
     backend through the public names, and the facade docs — which said the
     facades refuse Windows — describe the backends.
   - **Kernel buffering and resets** (`net/server`, `pkg/v1/server`): an 8 MiB
     write fit Windows' loopback buffers, so the write bound now writes until a
     write fails; a handler that answered without reading the request was
     reset, and Windows discards an unread reply on reset.

4. **A skip is allowed only where the fixture cannot exist**, its message says
   exactly why, and it follows an assertion of the platform's contract wherever
   one exists.

## Consequences / Semantics

- A change that breaks a package on Windows fails `e2e-cross` before merge.
- The Windows job carries the whole suite on every push, as macOS already did.
- Behaviour a caller can observe changed on Windows, always towards the
  platform's own answer: see §Breaking changes.

## Breaking changes

None to a published shape; `io.Closer` on a `NewMulti`/`FromConfig` Logger and
`lock.GrantsAnyone` are additions. Observable behaviour changes:

- `codec/multipart`: a filename carrying `\` decodes to its last element on
  Unix too (it already did on Windows).
- On Windows: `unixgram`/`unixpacket` listeners, the default journald dialer,
  a Windows-targeted `selfupdate` replacement, and a `queue.NewFile` over a
  safe directory now fail with `UNSUPPORTED_PLATFORM` instead of
  `LISTEN_FAILED`, `JOURNALD_OPEN_FAILED`, a post-download `ReplacementFailed`
  and `QUEUE_DIRECTORY_UNUSABLE` respectively.
- `net/server`: an auto-sized listener without `SO_REUSEPORT` no longer
  reports a reason; queue refusals gain an `observed` field.
- `lock` on Windows: a lock directory whose path the DACL reader cannot convert,
  or whose list it cannot walk to the end, is still accepted — and now LOGGED,
  as an unreadable list already was, since the reader names both.

## Alternatives considered

- **Skip every Windows failure.** It would have turned the inventory green
  and hidden four production bugs that had nothing to do with Windows being
  exotic: a codec that decoded one body two ways, a drop counter that did not
  count, a changed set that answered false, a queue that refused every
  directory.
- **Give the file sinks, the queue's states and vfs owner-only ACLs at
  creation** (`CreateFileW` with `SECURITY_ATTRIBUTES`). The principled closure
  of the `0600` gap on Windows, and a security boundary of its own with no
  local Windows to iterate on; named in the packages, deferred below.
- **Let the queue fail open on an unreadable list, as the lock does.** One
  rule for both callers of one reader. But ADR 0084 §D5 is an argument about
  costs, and the queue's costs are the other way round: see Decision 2.
- **Refuse `queue.NewFile` on Windows before its directory rules.** It would
  create nothing on a platform that refuses anyway, and leave the rules wrong
  for the day vfs gains a Windows backend; the rules run first instead, and are
  tested through real ACLs.
- **Release a `NewMulti` Logger's files in tests by forcing a collection.** It
  would have made the tests pass by leaning on finalizers and left the gap in
  the API for every caller.
- **Classify `unixgram` failures by errno at bind time.** An errno is a
  per-platform string of evidence; the family table is a build-tag split, with
  a Windows measurement that fails when the platform changes.

## Deferred

- **Owner-only access control at creation on Windows** for `logger/sink/file`,
  `writer/rotfile`, the queue's state directories and `vfs` — the gap named in
  each package's CLAUDE.md.
- **A Windows backend for `vfs`**, without which the durable queue does not run
  there.
- **`unixpacket` on darwin** fails as `LISTEN_FAILED` (`EPROTONOSUPPORT`,
  measured); the same refusal applies and is not made here.
- **`Adopt` on Windows** reports `SOCKET_ADOPT_FAILED` naming
  `UNSUPPORTED_PLATFORM` in a field, not as a typed code in the chain.
- **`FromConfig`** does not close the writers it opened when a later one fails;
  `NewMulti` does.

## Verification

- `e2e-cross` Windows job, step "Unit tests — every package on windows" —
  failing packages per run of this change, in order: 13
  ([run 36152499054](https://github.com/kitsunium/sdk/actions/runs/36152499054)),
  7 ([run 36154229595](https://github.com/kitsunium/sdk/actions/runs/36154229595),
  `net/client` among them: the flaky assumption the first run had passed), 1
  ([run 36156153568](https://github.com/kitsunium/sdk/actions/runs/36156153568)),
  **0** ([run 36156929003](https://github.com/kitsunium/sdk/actions/runs/36156929003)),
  still under `continue-on-error`; then green as a gate in the runs the pull
  request records.
- `GOWORK=off CGO_ENABLED=0 GOOS={windows,linux,darwin} GOARCH=amd64 go vet ./...`
  in all six modules; `go test` and `bazel test` of every touched package on
  darwin/arm64, including the race-off alloc lane for the logger; the repository
  guards; `ktn-linter` reports no finding the baseline did not.
- Mutation checks on darwin: restoring `FileName()` fails every backslash row of
  `TestAFileNameKeepsOnlyItsLastPathElementOnEveryOS`; restoring the shard reason
  fails `Test_resolveShards` on its no-`SO_REUSEPORT` arm.

## References

- [ADR 0094](0094-a-test-compiles-where-its-package-does.md), [ADR 0018](0018-sdk-cross-platform-portability.md)
- RFC 7578 §4.2 — a receiver drops the directory information in a filename
- `WSAEMSGSIZE` (10040) — `winsock2.h`; Windows' AF_UNIX supports `SOCK_STREAM` only
- `MoveFileEx` / `MOVEFILE_REPLACE_EXISTING` on a mapped image: `ERROR_ACCESS_DENIED`
