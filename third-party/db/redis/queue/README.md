# queue — a queue broker over Valkey or Redis streams

`import "github.com/kitsunium/sdk/third-party/db/redis/queue"` (module `github.com/kitsunium/sdk/third-party/db/redis`)

A `queue.Broker` over one stream and one consumer group, with the `Waker`,
`LeaseExtender`, `Rejecter`, `Releaser`, `DelayedNacker` and
`DeadLetterReader` siblings.

```go
broker, err := queue.NewStream(queue.StreamConfig{
	Client: rdb, Stream: "jobs", Group: "workers", CreateGroup: true,
	Policy: corequeue.PolicyValue{VisibilityTimeout: 30 * time.Second, MaxDeliveries: 5},
})
```

The leases are the broker's clock's, kept beside the stream; every verb is one
script, atomic on the server. One stream is one queue and one cluster slot.
See the package comment and `CLAUDE.md`.
