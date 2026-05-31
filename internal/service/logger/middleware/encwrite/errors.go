// Package encwrite — declares the sentinels returned by the byte-encrypting
// sink. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package encwrite

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// EncWriteSealFailed wraps a subkey-derivation or Seal failure raised while
	// encrypting a record's bytes before delivery to the downstream sink.
	EncWriteSealFailed = errs.Define(CodeEncWriteSealFailed, "ENC_WRITE_SEAL_FAILED",
		"Encrypting middleware could not seal the record",
		"service/logger/middleware/encwrite: Subkey derivation or Seal returned an error")

	// FramingFailed wraps a length-prefix framing failure — the sealed box is
	// too large for the 4-byte big-endian length prefix.
	FramingFailed = errs.Define(CodeFramingFailed, "FRAMING_FAILED",
		"Encrypting middleware could not frame the sealed record",
		"service/logger/middleware/encwrite: sealed box length exceeds the uint32 length prefix")
)
