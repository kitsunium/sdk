// Package cloudwatch — range 0.3.25.* (ADR 0015 writer registry; PP octet 0x19).
package cloudwatch

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.25.0 - 0.3.25.255
//
//: Canonical allocation (ADR 0006/0015): serial .2 ClientInit, serial .20 Put.
//: The two sentinels below are the only codes this writer ever mints.

// CodeCWClientInitFailed identifies a failure building the AWS CloudWatch Logs
// client from the supplied region / credentials at Open time.
const CodeCWClientInitFailed errs.Code = 0x00_03_19_02 // 0.3.25.2

// CodeCWPutFailed identifies a failed PutLogEvents delivery of a batched set of
// log events.
const CodeCWPutFailed errs.Code = 0x00_03_19_14 // 0.3.25.20

// CodeCWEventRejected identifies a single event dropped before delivery because
// its timestamp falls outside the PutLogEvents per-event bounds (older than 14
// days or more than 2 hours in the future). The event is dropped and surfaced
// via OnError instead of failing the whole batch.
const CodeCWEventRejected errs.Code = 0x00_03_19_15 // 0.3.25.21
