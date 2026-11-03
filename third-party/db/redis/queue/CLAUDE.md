<!-- updated: 2026-11-02T22:00:00Z -->
# third-party/db/redis/queue/

## Purpose

A `core/data/queue` `Broker` over a Valkey or Redis stream and one consumer
group (ADR 0054's deferred connector, ADR 0211, kitsunium/sdk#989), in the
go-redis vendor module named by its vendor (#984).

## Surface (design: `design/third-party/db/redis.yaml`)

`NewStream(StreamConfig) (queue.Broker, error)`; `StreamConfig` (`Client`,
`Clock`, `Stream`, `Group`, `Consumer`, `Policy`, `CreateGroup`); codes
`0.3.96.*` — `StreamMisconfigured`, `StreamUnreachable`, `GroupMissing`,
`CommandRefused` (each failed command with `QUEUE_BACKEND_FAILED` in its
trail). Siblings: `Waker`, `LeaseExtender`, `Rejecter`, `Releaser`,
`DelayedNacker`, `DeadLetterReader`.

## Keys, scripts, and whose clock

Every key shares the hash tag `{Stream}`: `:stream` (XADD), `:lease` (a sorted
set of entry ids by deadline), `:wait` (hand-backs by the instant they become
visible), `:count` and `:token` (hashes by entry id), `:dead` (the dead-letter
stream). `Receive` and every settlement are ONE Lua script each
(`scripts.go`): Receive reclaims the lapsed leases — dead-lettering those whose
deliveries are spent —, leases again the waiting entries whose instant has
come (XCLAIM to this consumer), then reads new ones through the group
(XREADGROUP). The instants are the BROKER's clock's, passed as text in
microseconds: the server's clock decides nothing, so a `ManualClock` drives
every deadline. A receipt is `rs1|<id>|<token>|<delivery>`; a lapsed or
replaced lease is `LEASE_EXPIRED`.

Ack is XACK and XDEL, so one stream is one queue: a second group on it would
see entries vanish. One stream is one slot of a cluster.

## Contents

| File | Role |
|---|---|
| `config.go` | `StreamConfig` and its refusals |
| `scripts.go` | the receive and settle scripts, the dead-letter function they share |
| `broker.go` | `streamBroker`, `NewStream`, `Publish`, `Receive`, the group's creation |
| `settle.go` | `Ack`, `Nack`, `NackAfter`, `Reject`, `Release`, `Extend` |
| `deadletter.go` | `DeadLetters` (XRANGE) and the record's decoding |
| `receipt.go`, `wake.go`, `cause.go`, `errors.go` | the receipt, the `Waker`, the cause, the wraps |
| `gen_codes.go`, `doc.go`, `gen_api_test.go` | written by kit gen from the design |

## Tests

- **Every lane** — `config_internal_test.go`: the refusals, the siblings, the
  receipts, the waker, the record decoding, an unreachable server's typed
  error, with no server.
- **Against Valkey, opt-in** — `e2e/integration/queue/redis`
  (`//go:build integration`, rule 12): the shared conformance rows
  (`e2e/integration/queue/brokerrows`) on a `ManualClock`, the race of two
  consumers over 1 000 messages, the missing group, the wake. No lane runs it
  until kitsunium/sdk#988.

## Do NOT

- Do arithmetic on an instant inside a script: a Lua number prints sixteen
  digits with an exponent. The broker computes, the script stores.
- Read XAUTOCLAIM's idle time as the lease: the lease is the broker's clock's.
