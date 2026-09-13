//go:build !windows

// Package entitlement - the unix half of the private-key permission gate. The
// mode bits mean what they say here, so they are worth refusing on.
package entitlement

import (
	"os"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// checkPrivateKeyMode refuses a private key readable beyond its owner.
//
// On a POSIX filesystem the permission bits are the access control, so a
// key at 0644 really is readable by every account on the machine and the
// possession proof it backs is theatre. Refusing loudly beats authorizing
// on a secret the whole machine can read.
func checkPrivateKeyMode(info os.FileInfo, path string) error {
	//: Any bit outside owner read/write means someone else can read the
	//: private half.
	if info.Mode().Perm()&^keyFileMode != 0 {
		//: Refuse loudly rather than authorize on a machine-wide secret.
		return refuse(coreent.ErrNoPossession,
			errs.String("stage", "check_key_mode"),
			errs.String("condition", "the private half is readable beyond its owner"),
			errs.String("path", path),
			errs.String("mode", info.Mode().Perm().String()))
	}
	//: Owner-only, as enrolment wrote it.
	return nil
}
