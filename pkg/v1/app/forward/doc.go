// Package forward is the public facade for store and forward: an export that
// waits on disk while the collector it is meant for is down, and a count of
// what it held, sent and lost (ADR 0185).
//
// A [Forwarder] holds each batch as one message of a queue (package
// [github.com/kitsunium/sdk/pkg/v1/data/queue]: a directory or a table, which
// outlive the process, or memory, which does not) and sends it later through a
// [Sender] its caller supplies. [Forwarder].Enqueue returns once the queue holds
// the batch — durably, and across a restart, only on a directory or a table;
// [Forwarder].Run sends, one batch at a time by
// default, each retried on the forwarder's clock while [Config].Retryable calls its
// failure retryable and the batch is younger than [Config].MaxAge — waiting the
// delay a collector imposes (a Retry-After) in full, otherwise a backoff from
// five to thirty seconds. A batch the sender refuses for good is dead-lettered in
// the queue; one that outlives its age is dropped. The queue is bounded in
// batches and in bytes, and [Config].WhenFull says what happens at the bound:
// [DropOldest] (the zero), [DropNewest], or [Block] for a bounded time.
// [Forwarder].Stats counts what is pending, sent, retried, dropped, expired and
// dead-lettered, and the last delay a collector imposed.
//
// For telemetry, the producer is an adapter put where the network exporter was,
// and the sender is the network exporter's DocumentExporter:
//
//	otlp, err := trace.NewOTLPHTTPExporter("otlp", trace.OTLPHTTPConfig{Endpoint: endpoint})
//	document, _ := otlp.(trace.DocumentExporter)
//	broker, err := queue.NewFile(queue.FileConfig{Dir: "/var/lib/app/spans", Policy: policy})
//	fw, err := forward.New(ctx, forward.Config{
//		Broker:    broker,
//		Send:      document.ExportDocument,
//		Retryable: trace.OTLPRetryable,
//	})
//	go fw.Run(ctx) // or under lifecycle.NewSupervisor
//	spans, err := forward.NewSpanExporter("otlp-durable", fw) // behind a trace.BatchSink
//
// One queue per signal: metrics take their own forwarder, through
// [NewMetricExporter] and the metrics exporter's DocumentExporter.
//
// A batch the forwarder keeps until a send — one no full queue dropped and no
// MaxAge expired, counted Dropped or Expired otherwise — is sent at least once:
// a process that dies between the collector's acceptance and the
// acknowledgement sends it again. A trace backend sees a duplicate span; a
// cumulative metric point repeats a value; a DELTA metric point is counted
// twice. A product exporting delta temporality through a forwarder accepts
// that, or exports cumulative.
package forward
