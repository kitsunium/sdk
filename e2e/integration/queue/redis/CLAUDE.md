<!-- updated: 2026-11-02T22:00:00Z -->
# e2e/integration/queue/redis/

The Valkey stream broker, `third-party/db/redis/queue` (ADR 0211,
kitsunium/sdk#989), against a real Valkey: `valkey/valkey:8` started by
testcontainers, or the server `VALKEY_ADDR` names.

| Test | What it holds |
|---|---|
| `TestIntegration_ValkeyStreamBroker_conformance` | the rows of `../brokerrows` on a ManualClock — the broker's leases are its own clock's, so no row sleeps —, the lapsed redelivery with `Deliveries == 2` and the race of two consumers over 1 000 messages included |
| `TestIntegration_ValkeyStreamBroker_groupMissing` | `GROUP_MISSING` without `CreateGroup` |
| `TestIntegration_ValkeyStreamBroker_wakesOnItsOwnPublication` | the `Waker`'s signal |

```sh
cd e2e && GOWORK=off go test -tags integration -race ./integration/queue/redis/
```

Open: it has never run in a lane — kitsunium/sdk#988's scheduled vendor lane
is where it belongs. It has run only in compilation (`go vet -tags
integration`) when this file was written.
