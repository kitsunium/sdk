<!-- updated: 2026-11-02T22:00:00Z -->
# third-party/db/redis/cache/

## Purpose

The cache domain's remote tier (ADR 0049's deferred distributed backend,
ADR 0211, kitsunium/sdk#829): a `core/data/cache` `Store` over Valkey or Redis
through go-redis, with its three siblings — `EntryFetcher`, `Tagger`,
`Loader` — so `internal/service/data/cache.NewChain` takes it as the far tier
behind a memory store, and a value survives a deploy and an invalidation
reaches every replica. It lives in the go-redis vendor module, named by its
vendor (`third-party/db/redis`, #984).

## Surface (design: `design/third-party/db/redis.yaml`)

`NewStore[V](Config) (cache.Store[V], error)`; `Config` (`Client`, `Codec`,
`Clock`, `Prefix`, `DefaultTTL`, `FillLease`, `FillPoll`); codes `0.3.95.*` —
`CommandFailed` (`CACHE_BACKEND_FAILED` in its trail), `ValueUndecodable`.
Misconfigurations are the domain's `CACHE_MISCONFIGURED`, refused entries its
`CACHE_ENTRY_REJECTED`, failed fills its `CACHE_FILL_FAILED`.

## Keys and scripts

Every key shares the hash tag `{Prefix}`: `{p}:v:<key>` the value,
`{p}:t:<key>` the entry's tags, `{p}:g:<tag>` the tag's keys, `{p}:l:<key>` a
fill lease. A write is one script (`scripts.go`): the value with its PX, the
entry's tag set, and its key in each tag's group — a group lives as long as
its longest entry. `FetchEntry` reads value, PTTL and tags in one script and
reports the REMAINING TTL. `InvalidateTag` scans the group (SSCAN, 128 a page)
and runs one script per page, counting the entries whose value was there.

## Loader

`Load` collapses misses within the process (`kernel/concur/singleflight`), as
the port promises. With `FillLease` positive it also takes `{p}:l:<key>` with
SET NX PX: the winner fills and releases its own lease; the others poll every
`FillPoll` on `Config.Clock` for the value, and compete again when the lease
is gone with no value. The lease is not fenced — ADR 0052's non-guarantee.

## Contents

| File | Role |
|---|---|
| `config.go` | `Config` and its refusals |
| `keys.go` | the key space under the hash tag |
| `scripts.go` | the five Lua scripts |
| `store.go` | `redisStore`, `NewStore`, `Fetch`, `FetchEntry`, `Set`, `Delete`, `InvalidateTag` |
| `load.go` | `Load` and the fill lease |
| `errors.go` | the wraps |
| `gen_codes.go`, `doc.go`, `gen_api_test.go` | written by kit gen from the design |

## Tests

- **Every lane** — `config_internal_test.go`: the refusals, the key space, the
  TTL mapping, the entry refusals and an unreachable server's typed error,
  with no server.
- **Against Valkey, opt-in** — `e2e/integration/cache/valkey`
  (`//go:build integration`, rule 12): the store, the remaining TTL, the tag
  counts over several scan pages, one fill across two replicas under the
  lease, and the chain with a memory near tier. It lives in the `e2e` module
  so testcontainers stays out of this module's `go.mod` (ADR 0157); see
  `e2e/integration/CLAUDE.md`. No lane runs it until kitsunium/sdk#988.

## Do NOT

- Put a brace in a prefix: it ends the hash tag and scatters the store.
- Read a value the codec cannot decode as a miss: it is `VALUE_UNDECODABLE`.
