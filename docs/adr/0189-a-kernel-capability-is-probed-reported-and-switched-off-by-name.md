# ADR 0189 — a kernel capability is probed, reported, and switched off by name

- **Status**: Accepted
- **Date**: 2026-10-25
- **Accepted**: 2026-10-25, on the owner's decisions of 2026-10-24 for the
  unified network work, R4 (class A split into A-kernel, on by default, and
  A-code, opt-in; `MIN_TIMEOUT` moved to class B; the accept stays
  single-shot), R5 (the environment only restricts) and R6 (an option not
  honoured is refused, never folded back). It can be revisited while the SDK
  is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Amends**: [ADR 0188](0188-the-operators-switches-only-restrict-and-state-says-what-the-server-runs-without.md)
  §D1 (the registry gains the kernel capabilities) and §Deferred (the optional
  capabilities join it here); [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D5 (the staged probe, and `DEFER_TASKRUN`, until now a test hook)
- **Related**: [ADR 0187](0187-the-handler-type-chooses-the-engine.md)
  §D10 (gates at the merge), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md)
  (design first), [ADR 0018](0018-sdk-cross-platform-portability.md) (the
  uapi in-tree)

## Context

The loop engine already used most of what makes io_uring fast — multishot
receive, the provided-buffer ring, direct descriptors — and epoll used
`EPOLLEXCLUSIVE`, each decided inside the probe and invisible from outside.
Three things were missing before the next kernel features could be turned on:

- **A name per capability.** An operator whose kernel misbehaves under one
  feature had only `KITSUNIUM_NET_DISABLE=iouring`: every other feature went
  with it.
- **A report.** `State` said which backend ran and the probe stage it reached,
  never which feature the kernel had, lacked, or was told not to use.
- **A way in for the features not proven yet.** `DEFER_TASKRUN`,
  `NO_SQARRAY`, registered ring descriptors and `SQ_REWIND` each change how a
  ring is driven; none may become a default before its own conformance, and
  `DEFER_TASKRUN`, the one already tried as a hook, stalls a secondary ring
  whose completions only its own enter would post.

## Decision

### D1 — The capabilities are in the registry, by class

`design/net.yaml` declares, beside the engines of ADR 0188, three blocks of
`Capability` values, each named in `KITSUNIUM_NET_DISABLE`:

- **Class A-kernel**, on once the probe validates them: `uring-multishot-recv`,
  `uring-pbuf`, `uring-direct`, `epoll-exclusive`, `tcp-nodelay`.
- **Class A-code**, off until `LoopValue.Capabilities` opts in and the probe
  validates them: `uring-defer-taskrun`, `uring-no-sqarray`, `uring-ring-fds`,
  `uring-sq-rewind`.
- **Class B**, declared, not built: `uring-send-zc`, `uring-bundle`,
  `uring-pbuf-inc`, `busy-poll`, `uring-zcrx`, `reuseport-cbpf`,
  `tcp-defer-accept`, `uring-min-timeout`. One joins class A-code only once
  its package's BENCH.md shows the threshold its own ADR sets; until then
  `LoopValue.Capabilities` refuses it.

`KITSUNIUM_NET_ENGINE` takes an engine only: a kernel capability there is an
unknown token. An unknown token, in either variable, switches every optional
capability off (R5: "no optional capability"), each row saying so. The
parser's registry and `check-net-default-change.sh` read the same blocks.

### D2 — A switched-off capability is never probed

Server hands the leaf the capabilities the environment switched off
(`loop.Config.Disabled`); the probe never asks the kernel about one. Some
switches change the backend rather than a flag:

- `uring-pbuf` off: the io_uring backend receives into provided buffers only,
  so io_uring is ruled out and never set up — an override, like
  `iouring` off, never a fallback.
- `uring-multishot-recv` off: every receive is single-shot, re-armed by the
  state machine after each completion.
- `uring-direct` off: connections live in the process table; neither the
  self-test nor the release check registers a table.
- `epoll-exclusive` off: every loop's epoll is woken per connection.
- `tcp-nodelay` off: the listeners keep the kernel's default; the portable
  backend clears the netpoller's on each connection.

### D3 — The probe descends, on throwaway rings, under the thread's lock

The probe runs on the loop's own thread, locked from the first setup to the
last close, on rings of 16 entries it closes before serving:

1. **Setup, flag by flag.** Every flag the switches allow first; on `EINVAL`
   the newest is given up and the setup retried: `SQ_REWIND`, then
   `NO_SQARRAY`, then `DEFER_TASKRUN`, then `SINGLE_ISSUER` — so a flag is
   never dropped before one that requires it (REWIND⇒NO_SQARRAY,
   DEFER⇒SINGLE). Each flag given up is reported absent.
2. **Features**: `EXT_ARG`, `NODROP`, `SINGLE_MMAP` required, as before;
   `FEAT_MIN_TIMEOUT` read for class B's row.
3. **`REGISTER_PROBE`** (5.6) for every op issued. `REGISTER_QUERY` (6.15) is
   not used: every kernel the backend serves answers the probe, and the
   query adds nothing the backend reads.
4. **Ring descriptors**: the probe's ring is registered with the thread
   (`IORING_REGISTER_RING_FDS`) and unregistered before it closes.
5. **`SQ_REWIND`'s partial submit**: two NOPs filled from SQE 0, one
   submitted, the other moved down and submitted, each completing once.
6. **The self-test** on the primary's flags, then the release check, as
   ADR 0186 describes.

`EEXIST` is a defect of the probe, never an answer of the kernel: it is what a
`SINGLE_ISSUER` ring returns when entered off the thread that set it up, which
a probe locked to its thread never does. A reason naming it is reported as
`probe defect`, and the conformance suite fails on one.

### D4 — The class A-code capabilities, each with its conformance

- **P3a, `uring-defer-taskrun`**: `DEFER_TASKRUN` with `SINGLE_ISSUER` on the
  primary ring only, the one the thread waits on. The secondaries keep
  `COOP_TASKRUN` and post their own completions, which their registered
  eventfd wakes the primary for; a deferred secondary would post nothing until
  entered with `GETEVENTS`, which the loop never does on it. On the primary,
  every submit carries `GETEVENTS` with `min_complete` zero, from the owner
  thread. Its test: traffic on a secondary ring alone, the round trip's median
  under half a tick; with `DEFER_TASKRUN` on every ring the second
  round trip times out.
- **P3b, `uring-no-sqarray`**: a layout variant, no SQ array to fill;
  `sqOff.array` reads zero.
- **P3c, `uring-ring-fds`**: each ring registered with its thread, at most 16
  (`IO_RINGFD_REG_MAX`), the ones past entered by descriptor; unregistered
  before the ring closes, since a registered ring outlives its close and its
  direct descriptors with it. Its test: the group closed, the peer reads the
  end.
- **P3d, `uring-sq-rewind`**: `SQ_REWIND`, which needs `NO_SQARRAY`: the
  submitter fills from SQE 0, and a partial submit moves what is left down
  before the next. It stays class A-code until a kernel 7.x lane runs it.

An opted-in capability the kernel lacks is `missing`, and a fault in `State`
(`capability`): the group serves without it, and says so (R6 refuses an
option no backend honours, which `LoopCapabilityRefused` does; a kernel that
lacks a feature is not a wiring mistake, and a refusal would make a binary
built for 7.x fail to start on 6.8). The environment wins over an opt-in.

### D5 — The report

`LoopStateValue.Capabilities` (`[]CapabilityValue`: name, class, status,
reason) lists every capability of D1 the loop reports, then server's
`tcp-nodelay` row — its socket probe, read back from each listener. Status is
`on`, `available`, `absent`, `missing`, `disabled` or `unprobed`. The
portable backend reports every row `unprobed`, or `disabled`.

### D6 — Never SQPOLL, and the gate

`SQPOLL` is never set, and no capability names it. The uapi numbers this
change adds are frozen by `TestURingABI`. Each leg of `net-kernel-gate.yml`
requires every class A capability its kernel has (`requiredCaps`), fail-closed:
a leg whose kernel lacks one fails. No leg requires `uring-sq-rewind` before a
7.x lane exists.

## Consequences / Semantics

- `core/net` gains seventeen `Capability` constants, `CapabilityValue`,
  `LoopStateValue.Capabilities`, `LoopValue.Capabilities` and
  `LoopCapabilityRefused` (`0.2.11.60`). `net/loop` gains
  `Config.Disabled`, `Config.DisabledBy`, `CheckCapabilities` and `Unprobed`.
  `pkg/v1/net/server` re-exports `CapabilityValue` and the four class A-code
  constants.
- A server with neither variable set serves as before: the class A-kernel
  capabilities were already in use; only the report is new.
- Measured on Ubuntu 6.8.0-147 and Debian 6.12.113 (arm64): every class A
  capability present but `uring-sq-rewind` (`EINVAL` at setup), the release
  check choosing direct descriptors on both; `FEAT_MIN_TIMEOUT` advertised on
  6.12, not on 6.8; the secondary-only round trip's median 64 µs and 50 µs
  (90th percentile 114 µs and 189 µs) under `DEFER_TASKRUN`.

## Breaking changes

None to a published behaviour: the new fields are empty for a value built by
hand, and the switches only act when set. `DEFER_TASKRUN`'s test hook is gone,
replaced by the opt-in.

## Alternatives considered

- **Turn the class A-code capabilities on by default where the probe finds
  them.** Rejected (R4): each changes how a ring is driven, and
  `DEFER_TASKRUN` on every ring already showed a stall the suite had missed.
- **Refuse an opted-in capability the kernel lacks.** Rejected: see D4; the
  fault says what runs without it.
- **One setup with every flag, and give them all up on `EINVAL`.** Rejected:
  it reports nothing per flag, and an older kernel would lose
  `SINGLE_ISSUER` for not knowing `SQ_REWIND`.
- **`REGISTER_QUERY`.** Deferred: see D3.

## Deferred

- Each class B capability, with its ADR and its BENCH.md threshold.
- `uring-sq-rewind` in class A-kernel, after a kernel 7.x lane.
- A class A-code capability in class A-kernel, each with its own
  `net:default-change` pull request.

## References

- Linux `include/uapi/linux/io_uring.h` (`IORING_SETUP_NO_SQARRAY` 1<<16,
  `IORING_SETUP_SQ_REWIND` 1<<20, `IORING_ENTER_REGISTERED_RING` 1<<4,
  `IORING_REGISTER_RING_FDS` 20, `IORING_FEAT_MIN_TIMEOUT` 1<<15),
  `io_uring/tctx.c` (`IO_RINGFD_REG_MAX`, `-EEXIST` for a `SINGLE_ISSUER`
  ring off its task), `io_uring/io_uring.c` (`SQ_REWIND` requires
  `NO_SQARRAY`, refuses `SQPOLL`).
- Impl: `design/net.yaml`, `internal/core/net/capability.go`,
  `internal/service/net/loop/` (`caps.go`, `uring_caps_linux.go`,
  `uring_ring_linux.go`, `uring_probe_linux.go`, `epoll_linux.go`),
  `internal/service/net/server/` (`operator.go`, `loop_engine_linux.go`,
  `loop_portable.go`, `stall.go`), `scripts/ci/check-net-default-change.sh`,
  `.github/workflows/net-kernel-gate.yml`.
