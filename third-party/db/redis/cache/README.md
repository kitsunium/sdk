# cache — the Valkey or Redis tier of the cache domain

`import "github.com/kitsunium/sdk/third-party/db/redis/cache"` (module `github.com/kitsunium/sdk/third-party/db/redis`)

A `cache.Store` over Valkey or Redis, with `EntryFetcher`, `Tagger` and
`Loader`, so a chain takes it as its far tier behind a memory store.

```go
far, err := cache.NewStore[User](cache.Config{
	Client: rdb, Codec: json.New(), Prefix: "users",
	DefaultTTL: 10 * time.Minute,
	FillLease: 5 * time.Second, FillPoll: 50 * time.Millisecond, // one fill across replicas
})
```

Every key of a store shares the prefix's hash tag, so a store lives on one
slot of a cluster. The cross-replica fill lease is not fenced. See the package
comment and `CLAUDE.md`.
