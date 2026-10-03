// Package i18n — the one error this package builds rather than declares. The
// domain's codes and sentinels — the port's verdicts and the outcomes only a
// concrete catalogue can produce — are declared in internal/core/app/i18n
// (ADR 0160); this file holds failLoad, which raises CATALOG_LOAD_FAILED over
// the filesystem's or the codec's own error.
package i18n

import (
	corei18n "github.com/kitsunium/sdk/internal/core/app/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitConfig matches sysexits EX_CONFIG (78), the status
// corei18n.CatalogLoadFailed carries: a catalogue that cannot be read is
// refused forever, and the fix is a deployment or a catalogue edit.
const exitConfig int = 78

// failLoad wraps a filesystem or codec cause onto
// [corei18n.CatalogLoadFailed], keeping the cause IN THE CHAIN rather than
// flattening it into a field.
//
// That distinction is the whole reason this helper exists instead of a
// String("cause", err.Error()): a caller needs to tell "there is no catalogue
// directory" from "the catalogue will not compile", and the only thing that
// answers that is errors.Is(err, fs.ErrNotExist). Rendering the cause into a
// field loses it — the text survives and the sentinel does not. Same shape and
// same reason as internal/service/data/vfs.failRead.
func failLoad(cause error, fields ...errs.FieldValue) error {
	//: the cause stays in the chain — that is the contract.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     corei18n.CodeCatalogLoadFailed,
		Reason:   "CATALOG_LOAD_FAILED",
		Public:   "The message catalogue could not be read or decoded",
		Private:  "service/app/i18n: a listing, a file read or a codec decode failed; the cause is wrapped, not replaced, so errors.Is against fs.ErrNotExist still answers",
		ExitCode: exitConfig,
	}, fields...)
}
