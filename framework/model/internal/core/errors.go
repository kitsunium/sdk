// The codes and sentinels of framework/model: range 0.4.1.* (ADR 0147 —
// layer 4 is the framework's). Each var's name equals its errs.Define
// Reason in SCREAMING_SNAKE form.

package core

import (
	"net/http"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// range: 0.4.1.0 - 0.4.1.255

// CodeInvalidID identifies an ID the grammar of [IDPattern] does not match.
const CodeInvalidID errs.Code = 0x00_04_01_01 // 0.4.1.1

// InvalidID is returned by [ParseID] for a string the ID grammar does not
// match. Its "part" field names what failed — the kind, the service name, the
// node name —, never the input.
var InvalidID = errs.Define(CodeInvalidID, "INVALID_ID",
	"The node identifier is not valid",
	"framework/model.ParseID: the input does not match IDPattern; the part field names the failing component",
	errs.WithHTTPStatus(http.StatusBadRequest))
