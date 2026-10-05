// Package s3 — the AWS adapter. This file (with cred_adapter.go) is the ONLY
// place that imports the AWS SDK; every other file is SDK-free and unit-tested
// through the uploadFunc seam. The live PutObject lives in an anonymous closure
// returned by newUploadFunc — confining the SDK here and keeping the batching
// logic testable without a network.
//
// Package s3 — range 0.3.35.* (ADR 0015 service slot 0x23).
//
// Package s3 — bridges the SDK's writer.CredentialProvider onto the AWS SDK's
// aws.CredentialsProvider so credentials refresh on each Retrieve. Split from
// client.go to keep one struct per file.
//
// Package s3 — declares the sentinels returned by the S3 writer. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Package s3 registers the "s3" writer factory. It lives under third-party/ —
// NOT in pkg/v1 — in the third-party/aws module it shares with the cloudwatch
// writer, so the AWS SDK it pulls never enters the module graph of pkg/v1
// consumers (nothing in the SDK requires that module; see ADR 0012), and a
// consumer of this writer inherits no other vendor (ADR 0157).
// Blank-importing the package self-registers the factory
// (and pulls the AWS SDK), so writer.Open("s3", logger.S3Config{…}) resolves
// only in builds that opt in. The factory wraps the batching terminal sink
// (s3sink.go) with the async middleware for a non-blocking ring + OnDrop, and
// with levelgate for the per-writer MinLevel.
//
// Package s3 — the batching terminal Sink. AWS-free: it talks to S3 only through
// the uploader seam, so the batching/flush logic is unit-tested with a fake
// (the real AWS adapter lives in client.go). Records are coalesced into one
// uploaded object per batch via the generic kernel batcher — flushed when the
// buffered bytes reach MaxBatchBytes, on the FlushEvery ticker, or on Flush /
// Close. The per-batch object key and the byte-weight both ride in the deliver
// closure / WeightOf, so the coalescing/flush/ticker machinery is the shared
// kernel/concur/batcher (ADR 0014), not a hand-rolled copy.
package s3
