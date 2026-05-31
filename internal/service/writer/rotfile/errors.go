// Package rotfile — declares the sentinels returned by the rotating file
// sink's constructor and Write / rotate paths. Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
package rotfile

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — used by the I/O sentinels so CLI
// consumers treat a rotating-file failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74

var (
	// RotFileOpenFailed wraps an os.OpenFile failure (or a refused symlink)
	// at construction time and on every reopen after a rotation.
	RotFileOpenFailed = errs.Define(CodeRotFileOpenFailed, "ROT_FILE_OPEN_FAILED",
		"Rotating file sink could not open the destination file",
		"service/writer/rotfile: os.OpenFile failed or the path is a symlink",
		errs.WithExitCode(exitIOErr))

	// RotFileRotateFailed wraps a failure during the rename shift, the gzip
	// compaction, or the 0600 chmod of a rotated sibling.
	RotFileRotateFailed = errs.Define(CodeRotFileRotateFailed, "ROT_FILE_ROTATE_FAILED",
		"Rotating file sink failed to rotate the destination file",
		"service/writer/rotfile: a rename, gzip, or chmod step in the rotation cycle failed",
		errs.WithExitCode(exitIOErr))

	// RotFileWriteFailed wraps the underlying *os.File.Write error at Write
	// time.
	RotFileWriteFailed = errs.Define(CodeRotFileWriteFailed, "ROT_FILE_WRITE_FAILED",
		"Rotating file write failed",
		"service/writer/rotfile: underlying *os.File returned an error",
		errs.WithExitCode(exitIOErr))
)

// wrapRotate wraps cause under the RotFileRotateFailed sentinel with a private
// diagnostic, keeping the rotation call sites terse. The cause never carries
// key bytes or path-derived secrets; only the supplied private string is added.
func wrapRotate(cause error, private string) error {
	//: single wrap point so every rotation failure carries the same code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeRotFileRotateFailed,
		Reason:  "ROT_FILE_ROTATE_FAILED",
		Public:  "Rotating file sink failed to rotate the destination file",
		Private: private,
	})
}
