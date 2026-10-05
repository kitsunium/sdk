package rotfile

import (
	corerotfile "github.com/kitsunium/sdk/internal/core/observe/logger/writer/rotfile"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wrapRotate wraps cause under the RotFileRotateFailed sentinel with a private
// diagnostic, keeping the rotation call sites terse. The cause never carries
// key bytes or path-derived secrets; only the supplied private string is added.
func wrapRotate(cause error, private string) error {
	//: single wrap point so every rotation failure carries the same code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corerotfile.CodeRotFileRotateFailed,
		Reason:  "ROT_FILE_ROTATE_FAILED",
		Public:  "Rotating file sink failed to rotate the destination file",
		Private: private,
	})
}
