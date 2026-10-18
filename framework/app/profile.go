package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// CodeProfileRead reports a profile of the process that could not be written
// or read back. It is in the framework's 0.4.2.* range.
const CodeProfileRead errs.Code = iapp.CodeProfileRead
