# queue — a queue broker over NATS JetStream

`import "github.com/kitsunium/sdk/third-party/nats/queue"` (module `github.com/kitsunium/sdk/third-party/nats`)

A `queue.Broker` over a JetStream stream and one durable pull consumer, with
the `Waker`, `LeaseExtender`, `Rejecter`, `DelayedNacker`, `OncePublisher`
and `DeadLetterReader` siblings.

```go
broker, err := queue.NewJetStream(queue.Config{
	Conn: nc, Stream: "JOBS", Subject: "jobs.work", DeadLetterSubject: "jobs.dead",
	Consumer: "workers", Timeout: 5 * time.Second, CreateStream: true,
	Policy: corequeue.PolicyValue{VisibilityTimeout: 30 * time.Second, MaxDeliveries: 5},
})
```

The broker, not the consumer's MaxDeliver, dead-letters a message with its
cause. See the package comment and `CLAUDE.md`.
