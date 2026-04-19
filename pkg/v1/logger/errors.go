// Package logger: errors.go declares pkg/v1/logger's sentinels. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package logger

import "github.com/kitsunium/sdk/internal/kernel/errs"

// WriterRequired is returned when NewText is called with Config.Writer==nil.
// Callers that want the "stderr convenience" use Default() instead.
var WriterRequired = errs.Define(CodeWriterRequired, "WRITER_REQUIRED",
	"Logger config requires an explicit writer",
	"pkg/v1/logger.NewText called with Config.Writer==nil; supply an io.Writer or use logger.Default()")
