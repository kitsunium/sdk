// Package cloudwatch — declares the sentinels returned by the CloudWatch writer.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package cloudwatch

// exitIOErr matches sysexits EX_IOERR — a remote delivery failure is an I/O
// problem rather than a generic internal software error (70).
const exitIOErr int = 74
