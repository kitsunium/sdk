// Package s3 — range 0.3.24.* (ADR 0012 awswriters/s3 block).
package s3

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.24.0 - 0.3.24.255

// CodeS3ClientInitFailed identifies a failure building the AWS S3 client from
// the supplied region / credentials at Open time.
const CodeS3ClientInitFailed errs.Code = 0x00_03_18_02 // 0.3.24.2

// CodeS3PutFailed identifies a failed PutObject upload of a batched log object.
const CodeS3PutFailed errs.Code = 0x00_03_18_14 // 0.3.24.20
