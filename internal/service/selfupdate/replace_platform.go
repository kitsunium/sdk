// Package selfupdate — the one platform the replacement step refuses, and why
// the refusal comes before anything is downloaded.
package selfupdate

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// canReplace refuses the replacement step on a Service that targets Windows.
//
// # Why Windows cannot complete it
//
// The step is temp file → chmod → rename over the running executable, and
// Windows will not let a file that is mapped as a running image be replaced:
// MoveFileEx(MOVEFILE_REPLACE_EXISTING) answers ERROR_ACCESS_DENIED. That maps
// to os.ErrPermission, which sent the old flow into the privilege fallback —
// `sudo -n mv`, a command Windows does not have — and ended in
// ReplacementFailed whose advice was to set the sudo opt-in: a Windows operator
// was told to authorise an escalation that does not exist there. ADR 0077
// documented the platform as unsupported for this step; nothing in the code
// said so.
//
// # Why before the download
//
// A refusal nothing can change belongs before the work it would waste, which
// is the rule canAuthenticate already follows for a build with no vendor key:
// a Windows host would otherwise fetch and authenticate a 10-30 MB archive and
// throw it away. It runs AFTER canAuthenticate, so a build that could never
// authenticate anything is still told that first, on every platform.
//
// # What it is
//
// The SDK's uniform UNSUPPORTED_PLATFORM (ADR 0018 §(a)), carrying the target
// platform and the tag, so errors.Is(err, proc.UnsupportedPlatform) answers on
// the public side. The known closures — rename the running image aside and
// write the new one in its place, or a post-exit installer — are recorded in
// ADR 0077 §Deferred and are not written here.
//
// It reads the Service's own goos rather than runtime.GOOS: the platform a
// Service was built for is the one its asset names already follow, and a test
// can construct one for Windows on any host.
func (u *Service) canReplace(tag string) error {
	//: every platform but one replaces by rename.
	if u.goos != "windows" {
		//: nothing to refuse.
		return nil
	}
	//: the platform's own answer, before a byte is fetched.
	return refuse(coreproc.UnsupportedPlatform,
		errs.String("goos", u.goos),
		errs.String("tag", tag),
		errs.String("step", "replace"))
}
