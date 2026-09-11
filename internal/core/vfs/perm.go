// Package vfs — the file-mode rule every implementation shares.
package vfs

import (
	"io/fs"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ValidatePerm reports whether perm is a mode this domain will apply, and
// returns [InvalidPermission] when it is not.
//
// Two refusals, and ADR 0031 is the reason for both.
//
// A ZERO mode is refused rather than defaulted. ADR 0031 admits clamping where
// a working default needs no explanation and demands a refusal where any
// SDK-chosen value would be arbitrary — and a file mode is the second case,
// unambiguously: 0644 and 0600 differ by who may read the bytes, the SDK does
// not know what the bytes are, and a mode of 0 is what an unfilled struct
// field looks like. Silently choosing would make the SDK the author of a
// security decision it has no information about.
//
// Bits OUTSIDE fs.ModePerm are refused by name. setuid, setgid, the sticky bit
// and the type bits are each a deliberate act; none of them should be
// reachable by mistyping a permission literal, and a mode argument that also
// carries them makes "0o4755 instead of 0o755" a one-character privilege
// escalation. A caller who genuinely wants one of those wants a call that says
// so, which this domain does not offer.
func ValidatePerm(perm fs.FileMode) error {
	//: two refusals, one verdict, so they are written as one guard: an unset
	//: mode is an unanswered question rather than a request for a default, and
	//: anything the permission triads cannot express is refused as written.
	//: The mode travels as a log-only field for diagnosis either way.
	if perm == 0 || perm&^fs.ModePerm != 0 {
		//: InvalidPermission.
		return errs.Wrap(InvalidPermission, errs.WrapParams{},
			errs.String("perm", perm.String()))
	}
	//: a plain, deliberate permission mode.
	return nil
}
