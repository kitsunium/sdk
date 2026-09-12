//go:build !windows

// Package entitlement - the unix half of the private-key permission gate. The
// mode bits mean what they say here, so they are worth refusing on.
package entitlement

import (
	"fmt"
	"os"
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
		return fmt.Errorf("%w: %s is readable beyond its owner", ErrNoPossession, path)
	}
	//: Owner-only, as enrolment wrote it.
	return nil
}
