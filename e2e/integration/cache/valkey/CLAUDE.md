<!-- updated: 2026-11-02T22:00:00Z -->
# e2e/integration/cache/valkey/

The Valkey cache tier, `third-party/db/redis/cache` (ADR 0211,
kitsunium/sdk#829), against a real Valkey: `valkey/valkey:8` started by
testcontainers, or the server `VALKEY_ADDR` names.

| Test | What it holds |
|---|---|
| `TestIntegration_ValkeyCache_storeAndEntry` | Fetch, Set, Delete; `FetchEntry`'s REMAINING TTL, `NoExpiry` and the default TTL; an expired entry; `VALUE_UNDECODABLE` |
| `TestIntegration_ValkeyCache_tags` | `InvalidateTag`'s count, a retagged entry out of its old tag, 300 entries over several scan pages |
| `TestIntegration_ValkeyCache_loadFillsOnceAcrossReplicas` | forty callers on two stores under a fill lease: one fill; `CACHE_FILL_FAILED` |
| `TestIntegration_ValkeyCache_isTheFarTierOfAChain` | `NewChain(memory, valkey)`: promotion into the near tier, a tag invalidated through both |

```sh
cd e2e && GOWORK=off go test -tags integration -race ./integration/cache/valkey/
```

Open: it has never run in a lane (kitsunium/sdk#988), only in compilation
when this file was written.
