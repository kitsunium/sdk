// Package cloudwatch — the AWS adapter. This file (with cred_adapter.go) is the
// ONLY place that imports the AWS SDK; every other file is SDK-free and unit-
// tested through the deliverFunc seam. The live PutLogEvents lives in an
// anonymous closure returned by newDeliverFunc.
//
// Package cloudwatch — range 0.3.25.* (ADR 0015 writer registry; PP octet 0x19).
//
// Package cloudwatch — bridges writer.CredentialProvider onto the AWS SDK's
// aws.CredentialsProvider so credentials refresh on each Retrieve. Split from
// client.go to keep one struct per file.
//
// Package cloudwatch registers the "cloudwatch" writer factory. It lives under
// third-party/ — NOT in pkg/v1 — in the third-party/aws module it shares with
// the s3 writer, so the AWS SDK it pulls never enters the module graph of
// pkg/v1 consumers (ADR 0012), and a consumer of this writer inherits no other
// vendor (ADR 0157).
// Blank-importing the package self-registers the factory (and pulls the AWS
// SDK), so writer.Open("cloudwatch",
// logger.CloudWatchConfig{…}) resolves only in builds that opt in. The factory
// wraps the batching terminal sink (cwsink.go) with async (non-blocking ring +
// OnDrop) and levelgate (per-writer MinLevel).
//
// Package cloudwatch — the batching terminal Sink. AWS-free: it talks to
// CloudWatch Logs only through the deliverFunc seam, so the batching/flush logic
// is unit-tested with a fake (the real AWS adapter lives in client.go). Each
// record becomes one log event (carrying its RecordEvent.Time); events are
// coalesced via the generic kernel batcher and delivered when the batch reaches
// the event cap, on the FlushEvery ticker, or on Flush / Close. The
// PutLogEvents chronological-order requirement is honoured by the deliver
// closure (a stable sort by timestamp), so the coalescing/flush/ticker
// machinery is the shared kernel/concur/batcher (ADR 0014), not a hand-rolled copy.
//
// Package cloudwatch — declares the sentinels returned by the CloudWatch writer.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package cloudwatch
