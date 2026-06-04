// Package s3 — range 0.3.35.* (ADR 0015 service slot 0x23).
package s3

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.35.0 - 0.3.35.255
//
// s3 was re-allocated off PP octet 0x18 (0.3.24.*) to 0x23 (0.3.35.*) to clear
// the V92/V99 collision with internal/service/codec/baseenc, which legitimately
// owns 0x18. The former CodeS3ClientInitFailed=0.3.24.2 and
// baseenc.CodeBaseEncUnmarshalFailed were the exact same uint32, conflating an
// S3 client-init failure with a base-encoding unmarshal failure under HasCode /
// NewPrefixMatcher routing. The codes were ambiguous, never a stable contract,
// so there is no compat alias (an alias would preserve the ambiguity).

// CodeS3ClientInitFailed identifies a failure building the AWS S3 client from
// the supplied region / credentials at Open time.
const CodeS3ClientInitFailed errs.Code = 0x00_03_23_02 // 0.3.35.2

// CodeS3PutFailed identifies a failed PutObject upload of a batched log object.
const CodeS3PutFailed errs.Code = 0x00_03_23_14 // 0.3.35.20
