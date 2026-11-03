# ADR 0211 — the data ports' vendor adapters: Valkey, JetStream, S3 and SQS

- **Status**: Accepted
- **Date**: 2026-11-02
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0054](0054-sdk-queue-domain.md) — resolves its Deferred
  "`third-party/` connector" for NATS and SQS, and adds a Valkey one;
  [ADR 0049](0049-cache-becomes-a-domain.md) — resolves its Deferred
  distributed backend with a remote tier; [ADR 0056](0056-sdk-vfs-domain.md) —
  the first `FullFS` outside `internal/service/data/vfs`, an object store with
  no rename; [ADR 0157](0157-one-module-per-vendor-released-with-the-sdk.md) —
  two vendor modules, `third-party/db/redis` and `third-party/nats`, and two
  packages more in the AWS module.
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)
  (a port grows by siblings), [ADR 0031](0031-policy-zero-values-are-never-inert.md)
  (refuse or clamp), [ADR 0052](0052-sdk-lock-domain.md) (a lease's
  non-guarantee said out loud), [ADR 0104](0104-an-idle-consumer-sleeps-until-there-may-be-work.md)
  (the `Waker`), [ADR 0110](0110-a-document-store-writes-one-entry-and-rests-as-one-file.md) (the docstore's file
  engine over `vfs`), [ADR 0171](0171-a-module-per-family-and-a-version-is-never-reused.md)
  (a vendor module requires `internal` only); kitsunium/sdk#829, #989, #990,
  #991, #1130; #984 (the go-redis module named by its vendor), #985 (the
  importable conformance suites), #988 (the scheduled vendor lane)

## Context

The queue, cache and vfs ports were frozen small so that an adapter outside
the SDK could stand behind them (ADR 0039), and their ADRs deferred those
adapters by name: ADR 0054 a `third-party/` broker (NATS, AMQP, Kafka, SQS),
ADR 0049 a distributed cache, ADR 0056 a filesystem with no rename. None
existed. A product whose replicas share no database or directory had no SDK
queue; a cache chain's second tier was a second memory store, a configuration
the domain's own BENCH.md says nobody deploys; a product on object storage
could keep no docstore. The continuous audit filed them as #829, #989, #990,
#991 and #1130.

Each of these servers keeps a clock of its own. A queue lease the server
times — SQS's visibility timeout, JetStream's AckWait — ends when the server
says, and a consumer whose lease ended there must still learn it before it
acknowledges somebody else's turn: the property ADR 0054 shares with the lock
domain.

## Decision

### 1. Where they live (ADR 0157)

| Module | Package | Port | Vendor | Codes |
|---|---|---|---|---|
| `third-party/db/redis` (new) | `cache` | `cache.Store`, `EntryFetcher`, `Tagger`, `Loader` | `go-redis/v9` | `0.3.95.*` |
| `third-party/db/redis` (new) | `queue` | `queue.Broker` and siblings | `go-redis/v9` | `0.3.96.*` |
| `third-party/nats` (new) | `queue` | `queue.Broker` and siblings | `nats.go` (`jetstream`) | `0.3.97.*` |
| `third-party/aws` | `vfs/s3` | `vfs.FullFS` | `aws-sdk-go-v2` (`service/s3`) | `0.3.98.*` |
| `third-party/aws` | `queue/sqs` | `queue.Broker` and siblings | `aws-sdk-go-v2` (`service/sqs`) | `0.3.101.*` |

The go-redis module is named by its vendor, as #984 asks, so these packages
land where #984 moves the writer and #984 renames nothing of theirs. Until it
does, two modules require go-redis — `third-party/db/writer/redis` and this
one —, the one exception to ADR 0157's rule, and #984 closes it. `0.3.99` and
`0.3.100` are left to #992 and #993, which named them first. Each module
requires `github.com/kitsunium/sdk/internal` only and replaces it with the
tree in development (ADR 0171); `go.work`, `MODULE.bazel` and
`design/sdk.yaml` name both new modules, and the census
(`scripts/ci/go-modules.sh`) and the release (`vendor_modules`) find them in
those files.

### 2. Whose clock is the lease

- **Valkey streams** keep the leases on the BROKER's clock. The deadlines live
  in a sorted set beside the stream, the hand-backs in a second one, the
  delivery counts and the lease tokens in two hashes, every key under the
  stream's hash tag, and every verb is one Lua script to which the broker
  passes its instants as text: the server's clock decides nothing, and
  XAUTOCLAIM's idle time is not read. A test therefore drives every deadline
  with a `ManualClock`, as for the SQL broker. A receipt names the entry, its
  lease's token and its delivery: a lapsed or replaced lease is
  `LeaseExpired`, a foreign receipt `UnknownReceipt`.
- **JetStream and SQS** keep the leases on the SERVER's clock. The broker reads
  its own clock BEFORE the fetch, so the deadline it hands out never falls
  after the server's, writes it into the receipt, and refuses an Ack, Nack,
  Extend or Reject at or past it before anything is sent. A receipt is good
  with the broker that issued it, whose table holds what the server needs to
  settle it — the delivery, the receipt handle — and the payload a dead letter
  needs; a renewal voids the receipt it replaces. The deadline is only as true
  as the two clocks agree, and each package says so.

### 3. The broker owns the dead letter (ADR 0054)

None of the three uses its server's own dead-lettering — JetStream's
MaxDeliver advisory, SQS's redrive policy —, which would move a message
without its cause. A Nack on the last delivery, or a Reject, publishes the
message with its cause on the dead-letter stream, subject or queue, then
removes it; a message whose leases all lapsed is dead-lettered by the Receive
that finds it past its budget, its Reason `LEASE_EXPIRED`. On Valkey that is
one script. On JetStream and SQS it is two requests, so a crash between them
dead-letters a message twice and never loses it.

### 4. What each one does not do

- **Valkey**: one stream is one slot of a cluster, and one queue — Ack deletes
  the entry, so a second group on the stream would see entries vanish. No
  `DelayedPublisher` and no `OncePublisher`: the group reads a new entry the
  moment it is added.
- **JetStream**: no `Releaser`, since the server's delivery count cannot be
  given back; `Extend` lasts the shorter of the asked duration and AckWait,
  since `InProgress` resets AckWait and nothing more; `PublishOnce` accepts
  only the stream's duplicate window, the one JetStream deduplicates over.
- **SQS**: no `Releaser`; no `OncePublisher`, which SQS offers on FIFO queues
  only, deferred with per-key ordering; no `Waker`, since long polling is the
  wait. A payload is at most 191 KiB, base64 in the body; a delay is at most
  fifteen minutes on a publication and twelve hours on a hand-back;
  `DeadLetters` samples a standard queue.
- **S3**: `WriteFile` and `WriteAtomic` are one `PutObject`, atomic per key,
  so no rename is faked. Modes are object metadata that S3 does not enforce;
  a directory is a marker object, or a prefix some key extends; a file is at
  most one `PutObject` (5 GiB), multipart upload being out of scope; every call
  is bounded by `Config.Timeout`, since the port takes no context. The
  filesystem answers `fs.ReadLinkFS` and `SyncDir` as the memory engine does.
- **The Valkey cache**: `Load` collapses the misses of one process, as the
  port promises, and those of several replicas only under `Config.FillLease`,
  a lease that is not fenced: a filler that outlives it may race a second
  one, and the later Set wins.

### 5. Conformance until #985

The rows of the conformance tables of `internal/service/data/queue` and
`internal/service/data/vfs` are restated beside each adapter until #985's
importable suites exist. The SQS broker and the S3 filesystem run them in
EVERY lane, over in-memory doubles of SQS and S3 that answer as the services
do — the S3 rows include `testing/fstest.TestFS` and the docstore's file
engine —, and against LocalStack under the `localstack` tag, as the AWS
writers' suites do. The Valkey and JetStream adapters run them in
`e2e/integration` against real servers — testcontainers, or `VALKEY_ADDR` and
`NATS_URL` — under the `integration` tag, since their tests would otherwise
put testcontainers in the modules' `go.mod` (ADR 0157). The shared queue rows
are `e2e/integration/queue/brokerrows`, the race of two consumers over a
thousand messages among them.

## Consequences

- A product on Valkey, NATS or AWS has an SDK queue, a remote cache tier and
  an object-store filesystem behind the ports it already programs against.
- Two vendor modules more in every census lane and in the release.
- The suites that need a server run in no lane until #988's scheduled vendor
  lane exists; the doubles keep the AWS adapters' rows in every lane.

## Deferred

- Redis Cluster placement across slots, and a stream shared by groups.
- The siblings a copy between brokers needs — `MessageReader`,
  `MessageWriter`, `DeadLetterManager`, `DeadLetterWriter` — on the three
  brokers.
- Benchmarks of the round trips (#829, #989, #991): they need a server, so
  they wait for #988's lane, which can run `make bench` beside one.
- GCS and Azure Blob filesystems, and AMQP: each a module of its own, of the
  same shapes.

## References

- kitsunium/sdk#829, #989, #990, #991, #1130; #984, #985, #988
- `third-party/CLAUDE.md`, `e2e/integration/CLAUDE.md`
