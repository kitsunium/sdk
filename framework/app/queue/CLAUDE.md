<!-- updated: 2026-10-17T22:00:00Z -->
# framework/app/queue — the queues, for a product that declares them

A subsystem of `framework/app`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/app/queue"` (ADR 0179 §1, step
4). It plugs in the brokers kit's consumers' queues run on — the SDK's queue
in memory, in the data directory and on a database —, their consumer, a
mailer's sealed outbox on a database and the migration of a queue's table
(`queueengine.Enable`). A product that declares no topic, no subscription, no
watch, no queued command and no mailer whose outbox is on a database links
none of them; one that declares one without the import is refused at the
start, every declaration at once, the refusal naming this import and carrying
`CodeSubsystemMissing` (0.4.2.83).

It imports `framework/app/store` (ADR 0179 Q4): a queue on a database joins
the store subsystem's transactions — the queues' SQL broker runs on the
transaction manager `storeengine` opened — and a sealed message is sealed under
data keys kit keeps in a store of its own. `framework/app` does not import
this package (ADR 0179 step 10; it did until then, Q8): a product that
declares a topic, a subscription, a watch or a queued command imports it,
and `kit gen` writes the line for a designed product (§5). `framework/app/mail`
imports it too (step 6, Q4): a mailer's outbox on a database is a queue of
this subsystem, which `mailengine` takes the SDK's broker of (`queueengine.BrokerOf`).

kit's own queue code — the consumers, the topics' publication, the queued
commands, the watches, the mailer's outbox and the data copy of a queue —
names no SDK type and stays in the core; it is reached only from those
declarations, so a program that declares none links none of it, and this
package needs no `Enable` of the core's (unlike `framework/app/store`'s
`EnableStores`).

## Contents

| File | Holds |
|---|---|
| `queue.go` | the package documentation, the blank import of `framework/app/store`, and `enable`, which calls `queueengine.Enable` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour and exports nothing: the call it makes is into `framework/internal/app/queueengine`; the refusal naming the import lives in the core (`subsystem.go`, `queue_plug.go`, `subsystems_internal_test.go`).
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/queue/ && GOWORK=off go test ./kit/ -run 'ImportsTheQueues|TheSmallestProduct' && GOWORK=off go test ./internal/kit/ -run 'AQueue'
```
