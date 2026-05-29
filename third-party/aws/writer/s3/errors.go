// Package s3 — declares the sentinels returned by the S3 writer. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package s3

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — a remote upload failure is an I/O
// problem rather than a generic internal software error (70).
const exitIOErr int = 74

var (
	// ClientInitFailed wraps an AWS SDK config/client construction failure at
	// Open time (bad region, unreachable credential source, …).
	ClientInitFailed = errs.Define(CodeS3ClientInitFailed, "CLIENT_INIT_FAILED",
		"S3 writer could not initialise its AWS client",
		"third-party/aws/writer/s3.Open: aws config/client construction failed")

	// PutFailed wraps a failed PutObject upload of a batched log object.
	PutFailed = errs.Define(CodeS3PutFailed, "PUT_FAILED",
		"S3 writer failed to upload a log batch",
		"third-party/aws/writer/s3: PutObject returned an error while flushing a batch",
		errs.WithExitCode(exitIOErr))
)
