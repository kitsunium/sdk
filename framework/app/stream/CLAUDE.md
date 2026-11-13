<!-- updated: 2026-11-12T00:00:00Z -->
# framework/app/stream — the streams, for a product that declares a streamed topic

A subsystem of `framework/app`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/app/stream"` (issue #826, the
shape of ADR 0179 step 4). It plugs in the log a streamed topic
(`Topic.Streamed`) appends each message to once — whatever its number of
subscriptions, where a topic that is not streamed puts a copy in each
subscription's queue —, the consumer groups its subscriptions read it as
(`app.FromBeginning`, `app.FromNow`, `app.FromTime`) and the migration of a
stream's tables (`streamengine.Enable`). A product that declares no streamed
topic links none of the SDK's stream; one that declares one without the
import is refused at the start, the topic and each of its subscriptions, the
refusal naming this import and carrying `CodeSubsystemMissing` (0.4.2.83). A
streamed topic needs no queue: `framework/app/queue` is not required for it.

It imports `framework/app/store`: a stream on a database joins the store
subsystem's transactions — an append in `app.Transact` is held to the commit
and made in the transaction when the stream lives on its database.
`framework/app` does not import this package.

## Contents

| File | Holds |
|---|---|
| `stream.go` | the package documentation, the blank import of `framework/app/store`, and `enable`, which calls `streamengine.Enable` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour and exports nothing: the call it makes is into `framework/internal/app/streamengine`; the refusal naming the import lives in the core (`subsystem.go`, `stream_plug.go`).
- The call runs from a blank package-level value (`var _ = …()`), never an `init`.

## Verify

```sh
cd framework && GOWORK=off go vet ./app/stream/ && GOWORK=off go test ./app/ -run 'TheSmallestProduct|TheCore' && GOWORK=off go test ./internal/app/ -run 'Streamed'
```
