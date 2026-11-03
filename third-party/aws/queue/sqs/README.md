# sqs — a queue broker over Amazon SQS

`import "github.com/kitsunium/sdk/third-party/aws/queue/sqs"` (module `github.com/kitsunium/sdk/third-party/aws`)

A `queue.Broker` over one SQS queue and its dead-letter queue, with the
`LeaseExtender`, `Rejecter`, `DelayedNacker`, `DelayedPublisher` and
`DeadLetterReader` siblings.

```go
broker, err := sqs.New(sqs.Config{
	QueueURL: jobsURL, DeadLetterQueueURL: deadURL, Region: "eu-west-3",
	Credentials: provider, // a writer.CredentialProvider
	Policy: queue.PolicyValue{VisibilityTimeout: 30 * time.Second, MaxDeliveries: 5},
	WaitTime: 20 * time.Second,
})
```

The broker, not SQS's redrive policy, dead-letters a message with its cause.
A lease is refused at its deadline on the broker's clock, whatever SQS would
do. The package comment and `CLAUDE.md` give the mapping and the limits.
