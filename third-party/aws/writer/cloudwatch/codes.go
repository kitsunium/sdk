// Package cloudwatch — range 0.3.25.* (ADR 0012 awswriters/cloudwatch block).
package cloudwatch

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.25.0 - 0.3.25.255

// CodeCWClientInitFailed identifies a failure building the AWS CloudWatch Logs
// client from the supplied region / credentials at Open time.
const CodeCWClientInitFailed errs.Code = 0x00_03_19_02 // 0.3.25.2

// CodeCWPutFailed identifies a failed PutLogEvents delivery of a batched set of
// log events.
const CodeCWPutFailed errs.Code = 0x00_03_19_14 // 0.3.25.20
