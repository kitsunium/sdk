package listen

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxNameBytes is systemd's FDNAME_MAX: the longest name LISTEN_FDNAMES
// carries.
const maxNameBytes int = 255

// unknownName is what a descriptor handed without a name is announced as, as
// sd_listen_fds_with_names(3) names every descriptor when LISTEN_FDNAMES is
// absent.
const unknownName string = "unknown"

// exitUsage is sysexits.h EX_USAGE (64), restated to wrap the central
// INVALID_FD_NAME sentinel without re-Defining the code.
const exitUsage int = 64

// NamedFileValue is one descriptor PrepareFiles hands a child, and the name
// LISTEN_FDNAMES gives it: a socket a service manager bound, or one a
// descriptor store kept with the FDNAME= its service sent.
type NamedFileValue struct {
	// Name is the descriptor's name; empty is announced as "unknown".
	Name string
	// File is the descriptor; PrepareFiles neither duplicates nor closes it.
	File *os.File
}

// CheckName refuses, with InvalidFDName, a descriptor name LISTEN_FDNAMES cannot
// carry — systemd's fdname_is_valid: at most 255 bytes, every byte printable
// ASCII, no ':', the list's separator. The empty name is valid; Prepare and
// PrepareFiles announce it as "unknown". It is the check FileDescriptorName=
// and FDNAME= values pass, on every platform.
func CheckName(name string) error {
	//: systemd's FDNAME_MAX.
	if len(name) > maxNameBytes {
		//: too long.
		return wrapInvalidName(name, "longer than FDNAME_MAX")
	}
	//: byte by byte: printable ASCII, no separator.
	for i := range len(name) {
		c := name[i]
		//: a control byte, DEL or beyond, or the list's separator.
		if c < ' ' || c > '~' || c == ':' {
			//: refused.
			return wrapInvalidName(name, "not printable ASCII, or a ':'")
		}
	}
	//: a name LISTEN_FDNAMES carries.
	return nil
}

// announced is the name LISTEN_FDNAMES gives a descriptor named name.
func announced(name string) string {
	//: no name.
	if name == "" {
		//: libsystemd's default.
		return unknownName
	}
	//: as given.
	return name
}

// wrapInvalidName is the central InvalidFDName sentinel naming the name and
// why it was refused.
func wrapInvalidName(name, why string) error {
	//: restate the INVALID_FD_NAME sentinel fields; no cause, only the name.
	return errs.Wrap(nil, errs.WrapParams{
		Code:     coreproc.CodeInvalidFDName,
		Reason:   "INVALID_FD_NAME",
		Public:   "The descriptor name is not valid",
		Private:  "service/proc/systemd/listen: a descriptor name is longer than FDNAME_MAX, not printable ASCII, or holds a ':'",
		ExitCode: exitUsage,
	}, errs.String("name", name), errs.String("why", why))
}
