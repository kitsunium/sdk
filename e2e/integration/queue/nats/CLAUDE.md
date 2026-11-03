<!-- updated: 2026-11-02T22:00:00Z -->
# e2e/integration/queue/nats/

The JetStream broker, `third-party/nats/queue` (ADR 0211, kitsunium/sdk#990),
against a real nats-server with JetStream: `nats:2.11` started by
testcontainers with `-js`, or the server `NATS_URL` names.

| Test | What it holds |
|---|---|
| `TestIntegration_JetStreamBroker_conformance` | the rows of `../brokerrows` on the wall clock — the server keeps AckWait, so a row sleeps where it lets a lease lapse —, the dead-letter rows and the race of two consumers over 1 000 messages included |
| `TestIntegration_JetStreamBroker_publishOnceDeduplicates` | `PublishOnce` twice under one key: one message in `StreamInfo.State.Msgs` |
| `TestIntegration_JetStreamBroker_missingStreamIsTyped` | `STREAM_MISSING` without `CreateStream` |

```sh
cd e2e && GOWORK=off go test -tags integration -race ./integration/queue/nats/
```

The server runs as a container rather than in-process
(`nats-server/v2/test`): its packages would otherwise enter this module's
graph for one suite. Open: it has never run in a lane (kitsunium/sdk#988),
only in compilation when this file was written.
