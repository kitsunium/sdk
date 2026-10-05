// Package s3 — declares the sentinels returned by the S3 writer. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package s3

// exitIOErr matches sysexits EX_IOERR — a remote upload failure is an I/O
// problem rather than a generic internal software error (70).
const exitIOErr int = 74
