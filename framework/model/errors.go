// The codes and sentinels of framework/model: range 0.4.1.* (ADR 0143 —
// layer 4 is the framework module). Each var's name equals its errs.Define
// Reason in SCREAMING_SNAKE form.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
	errs "github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// CodeInvalidID identifies an ID the grammar of [IDPattern] does not match.
	CodeInvalidID errs.Code = core.CodeInvalidID // 0.4.1.1
)

// InvalidID is returned by [ParseID] for a string the ID grammar does not
// match. Its "part" field names what failed — the kind, the service name, the
// node name —, never the input.
var InvalidID *errs.Error = core.InvalidID
