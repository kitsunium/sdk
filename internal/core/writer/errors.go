// Package writer — declares the sentinels returned by the registry and shared
// across factories. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. CodeDuplicateRegistration and CodeWriterNil are
// surfaced via panic at boot (see registry.go), not as *Error sentinels.
package writer

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// WriterUnknownName is returned by Open when no factory is registered
	// under the requested Name — typically a missing blank-import.
	WriterUnknownName = errs.Define(CodeWriterUnknownName, "WRITER_UNKNOWN_NAME",
		"No writer is registered under that name",
		"core/writer.Open: name absent from registry; blank-import the writer's package to register it")

	// WriterConfigInvalid is the shared sentinel every factory returns when
	// the supplied Config is of the wrong concrete type. Factories attach an
	// errs Field naming the writer so the offender is identifiable.
	WriterConfigInvalid = errs.Define(CodeWriterConfigInvalid, "WRITER_CONFIG_INVALID",
		"Writer configuration has the wrong type",
		"core/writer factory received a Config of an unexpected concrete type")
)
