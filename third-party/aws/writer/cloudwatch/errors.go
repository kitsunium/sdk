// Package cloudwatch — declares the sentinels returned by the CloudWatch writer.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package cloudwatch

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — a remote delivery failure is an I/O
// problem rather than a generic internal software error (70).
const exitIOErr int = 74

var (
	// ClientInitFailed wraps an AWS SDK client construction failure at Open
	// time (bad region, unreachable credential source, …).
	ClientInitFailed = errs.Define(CodeCWClientInitFailed, "CLIENT_INIT_FAILED",
		"CloudWatch writer could not initialise its AWS client",
		"third-party/aws/writer/cloudwatch.Open: aws client construction failed")

	// PutFailed wraps a failed PutLogEvents delivery of a batch.
	PutFailed = errs.Define(CodeCWPutFailed, "PUT_FAILED",
		"CloudWatch writer failed to deliver a log batch",
		"third-party/aws/writer/cloudwatch: PutLogEvents returned an error while flushing a batch",
		errs.WithExitCode(exitIOErr))
)
