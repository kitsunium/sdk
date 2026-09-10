# internal/service/queue/

## Purpose

The two brokers implementing `internal/core/queue` (ADR 0054) and the consumer
engine that drives a `Handler` against either: `NewFile` (the durable one,
whose state is a directory), `NewMemory` (the test double), and `Consume` (the
pull loop that runs handlers on goroutines it owns).

## Contents

| File | Holds |
|---|---|
| `queue.go` | package doc, the dead-letter `causeValue` reduction, `randomHex`, the two shared guards (`checkBatch`, `checkSize`) |
| `memory.go` | `NewMemory` and the in-heap broker: the heap-ordered lease expiry, the ready list ordered at insertion |
| `memory_config.go` / `mem_record.go` / `lease_expiry.go` | `MemoryConfig` and the two values the memory broker keeps |
| `file.go` | `NewFile`, `Publish`, `Ack`, receipt resolution, `entriesOf` |
| `file_config.go` | `FileConfig`, the directory preparation and the world-writable refusal |
| `file_name.go` | the NAME grammar — the durable broker's entire state machine |
| `file_receive.go` | `Receive`, the reclaim scan, the rename that IS the exclusion |
| `file_dead.go` | `Nack`, `Extend`, `DeadLetters`, the burial, the dead-letter record's encoding |
| `consume.go` | `Consume`, the pull loop, the panic guard |
| `consume_config.go` | `ConsumerConfig`, the idempotence assertion, the clamps |
| `codes.go` / `errors.go` | the four `0.3.53.*` codes and their sentinels |

## Why the durable broker's state is a FILENAME

Because `rename(2)` is the only operation a POSIX filesystem offers that
changes a fact indivisibly and refuses for the loser. Three directories are the
three states — `ready/`, `inflight/`, `dead/` — and every transition is one
rename. So:

- **Exclusion is free and needs no lock.** Two consumers reaching for the same
  message both attempt the same rename; the kernel gives it to one and gives
  the other `ENOENT`. There is no lock file, nothing a dead process could hold,
  and — unlike `flock(2)`, which ADR 0052 MEASURED giving zero exclusion
  between goroutines sharing one open file description — it resolves identically
  for two goroutines and for two processes.
- **Recovery needs no daemon.** A lease deadline and a delivery count are both
  in the name, so a consumer that is SIGKILLed leaves a directory entry any
  other process can read and act on. There is no sweeper goroutine: a lapsed
  lease is noticed by the next `Receive`, in whatever process makes it. A
  sweeper would be a fourth thing that can die, and it would need electing.
- **A lexicographic sort is a chronological sort.** Every name opens with a
  19-digit zero-padded instant, so `fs.ReadDir`'s ordering is FIFO for free.

## Where `internal/service/vfs` is used, and where it stops

`vfs.AtomicWriter.WriteAtomic` does the two writes that must be atomic AND
durable: the enqueue, and the dead-letter record. That is exactly ADR 0056's
five steps — temporary in the same directory, write, flush the FILE, rename,
flush the DIRECTORY — with failure paths that were tested by injecting a
failure at each one and comparing the destination's hash. Rewriting that here
would duplicate subtle code, and the first thing to rot would be the failure
paths.

It stops at `corevfs.WritableFS`, which has `WriteFile`, `MkdirAll`, `Remove`
and `RemoveAll` and **no `Rename`**. That is not worked around: the port is
FROZEN (ADR 0039), so a fifth method to carry this domain's state machine would
break every downstream implementation at compile time. The transitions go
through `os.Root` directly — the same confinement mechanism `vfs` itself uses
— and the boundary between the two is the boundary between "publish a whole
file" and "move one".

## Where the flushes are, and where they deliberately are not

`Publish` flushes. Nothing else does, and that is a decision rather than an
omission: every non-durable outcome of a receive, an ack or a nack degrades
into a REDELIVERY, which at-least-once already permits and every consumer is
already obliged to survive. Paying a device round trip to make a duplicate
slightly less likely would buy nothing the contract does not already give away.

`BENCH.md` says what the one flush costs: **3.3 ms of a 3.3 ms round trip** —
the enqueue IS the round trip, to within the noise — and it is independent of
the payload size across a 1 024× range.

## Consume, and the one piece of ceremony

`ConsumerConfig.HandlerIsIdempotent` must be `true`; its zero value is refused
with `ConsumerMisconfigured`. This is `resilience.HedgeConfig.Idempotent`'s
instrument for its reason: the queue delivers at least once, the SDK cannot
check whether a handler survives that, so the caller asserts it where a
reviewer sees it — and `grep -rn HandlerIsIdempotent` enumerates every place in
a codebase where somebody promised it.

`Parallelism`, `BatchSize` and `PollInterval` are all CLAMPED, because each has
one sensible reading at zero and none of them is dangerous.

## Where this package departs from `service/events`, and why

`events` joins its `ListenerFailed` verdict beside the listener's cause,
because `Publish` RETURNS the aggregate and a caller has to be able to ask "did
anything fail?" without knowing every code every listener might produce.

Here the aggregate's destination is a DEAD-LETTER RECORD, which already says "a
handler failed" by existing and has structured fields for the rest. So a
handler's error travels to `Nack` unmodified, there is deliberately no
`HandlerFailed` sentinel, and `TestAFailingHandlerIsRetriedAndThenDeadLettered`
asserts that the HANDLER'S own reason is what reaches the store. A verdict
wrapped or joined around the cause would only displace it.

A handler PANIC is different and does get a sentinel: `HandlerPanicked`,
recovered on the worker's goroutine with the originating stack, with the
recovered value travelling as a FIELD so a `panic(someSentinel)` cannot hijack
the code.

## Tests worth knowing about

- **`TestAKilledConsumerLosesItsLeaseAndTheMessageComesBack`** is the domain's
  central proof and it is not a `Nack` test. It re-executes this test binary as
  a child process, waits for the child to announce a lease, asserts that the
  PARENT (a different process) can see nothing, then SIGKILLs the child — no
  deferred function, no signal handler, no flush — and asserts the message
  comes back with `Deliveries == 2`. Every fact that recovery needs had to have
  been on the disk, because the process that knew them is gone.
  `TestTheVictimConsumer` is the child; it self-skips unless
  `KTNQ_VICTIM_QUEUE_DIR` is set, so it is discovered by `go test ./...` like
  any other test and needs no build tag, no `manual` target and no compensating
  lane (CLAUDE.md rule 12).
- **`TestASingleRetryReordersTheStream`** demonstrates the thing everyone
  discovers in production, on purpose, so it is documented rather than found.
- **The conformance suite is table-driven over BOTH brokers.** A double nobody
  checks against the real thing is a double that has already drifted.
- **`TestAStrayFileInTheQueueDirectoryIsNeverDelivered`** plants a
  `.vfs-<hex>.tmp` in `ready/` — the temporary a CONCURRENT atomic publication
  is genuinely writing there — and asserts the scan skips it. Delivering it
  would hand a consumer a truncated payload under a receipt naming a file about
  to be renamed away.

## Do NOT

- Add a `Close`. The durable broker holds one `os.Root` (the `service/vfs`
  precedent) and no per-message handle, and nothing about the queue's contents
  lives in this process — which is what makes it genuinely inter-process.
- Flush on `Ack`, `Nack` or `Receive`. See above; it buys nothing.
- Add a sweeper goroutine, a timer, or a background reclaim. Expiry is noticed
  by whoever looks next, in both brokers, deliberately.
- Take a lock in the file broker. `rename(2)` is the exclusion, and ADR 0052
  measured why a `flock` would not be.
- Optimise `Publish/file`'s 41 allocations. They sit next to 3 ms of `fsync`;
  see `BENCH.md`.
- Range over the in-flight map in `memory.go` looking for expiries. That was
  the original code and it measured 105 µs per lease; the `kernel/heap` min-heap
  that replaced it measures 1.83 µs. `BENCH.md` has both numbers.

## Reference

- ADR 0054 — `docs/adr/0054-sdk-queue-domain.md`
- ADR 0053 §D1 — the frontier this domain is the right-hand column of
- ADR 0056 — `vfs`, whose `WriteAtomic` is this package's durable publish
- ADR 0052 — `lock`, whose `flock(2)` measurement is why there is no lock here
- `internal/core/queue/CLAUDE.md` — the port and the five decisions
- `BENCH.md` — the cost of durability, and the two defects the numbers found
