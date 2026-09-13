//go:build windows

// Package lock — the Windows half of "do not follow an indirection at the lock
// path".
//
// # CreateFileW followed reparse points, and that was read rather than assumed
//
// os.OpenFile reaches CreateFileW through syscall.Open, and syscall.Open sets
// FILE_FLAG_OPEN_REPARSE_POINT for exactly ONE createmode: CREATE_NEW, which
// is O_CREAT|O_EXCL. This locker opens O_CREATE|O_RDWR, which is OPEN_ALWAYS,
// which does not get the flag — so before this change the open followed a
// symbolic link or a junction planted at the lock path, the same defect the
// Unix side was measured to have. Read in the toolchain this repository pins
// (go1.27.0, src/syscall/syscall_windows.go), not inferred from behaviour.
//
// # The flag OPENS the link, it does not refuse it — so the check is the pair
//
// FILE_FLAG_OPEN_REPARSE_POINT means "give me a handle to the reparse point
// itself" rather than "fail if there is one". Used alone it fixes the
// redirection — the flock and the ledger stop landing on the attacker's file —
// and leaves the lock sitting on a link the attacker still owns and can
// retarget. So the flag is paired with GetFileInformationByHandle: any handle
// whose attributes carry FILE_ATTRIBUTE_REPARSE_POINT is closed and refused.
// The flag is what makes the check possible (without it the handle is the
// TARGET, which has no reparse attribute and nothing to notice); the check is
// what turns a redirect into a refusal.
//
// # No new dependency, and no hand-rolled CreateFile
//
// golang.org/x/sys is banned SDK-wide, and ADR 0081 bound LockFileEx from
// kernel32 with syscall.NewLazyDLL rather than importing it. Nothing of the
// kind is needed here: go1.27's syscall.Open passes the high 12 bits of its
// flag word through to CreateFileW's dwFlagsAndAttributes, and
// FILE_FLAG_OPEN_REPARSE_POINT is in its validFileFlagsMask — so the flag
// rides the ordinary os.OpenFile call.
//
// That matters beyond tidiness. ADR 0081 §D5 accepts every directory on
// Windows on the strength of one sentence: "os.OpenFile reaches CreateFileW
// WITHOUT FILE_SHARE_DELETE, so a held lock file can be neither deleted nor
// renamed whatever the ACL says". A hand-rolled CreateFile would have had to
// restate that share mode — and a hand-rolled share mode that drifts is how
// the Windows directory rule silently stops being justified. The open stays
// os.OpenFile, so the sentence stays literally true.
package lock

import (
	"os"
	"strconv"
	"syscall"
)

// openReparsePoint is FILE_FLAG_OPEN_REPARSE_POINT (winbase.h), passed in the
// high 12 bits of the flag word that syscall.Open forwards to CreateFileW.
const openReparsePoint int = syscall.FILE_FLAG_OPEN_REPARSE_POINT

// attrBase is the radix the refusal renders the attribute word in. Hexadecimal
// is what winnt.h spells FILE_ATTRIBUTE_* in, so a reader can compare the
// reported value against the header without converting it first.
const attrBase int = 16

// openLockFile opens the lock file, refusing a reparse point planted at path.
//
// It reports [LockPathRedirected] for an indirection and [LockBackendFailed]
// for anything else, which is the same pair the Unix half reports — the
// sentinel is the contract, the mechanism is not.
func openLockFile(path string) (file *os.File, err error) {
	opened, openErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR|openReparsePoint, lockFileMode)
	//: the open failed. A junction is refused here rather than below, because
	//: a DIRECTORY reparse point cannot be opened for read-write at all
	//: without FILE_FLAG_BACKUP_SEMANTICS: the call returns ERROR_ACCESS_DENIED
	//: and never reaches the handle check.
	if openErr != nil {
		//: LOCK_PATH_REDIRECTED or LOCK_BACKEND_FAILED.
		return nil, classifyOpenFailure(path, openErr)
	}
	//: the handle may be the LINK itself; the caller owns it only if it is not.
	return refuseReparseHandle(opened, path)
}

// refuseReparseHandle returns file unless its handle is a reparse point, in
// which case it is closed and refused.
func refuseReparseHandle(file *os.File, path string) (opened *os.File, err error) {
	var info syscall.ByHandleFileInformation
	//: the question is asked of the HANDLE, not of the path, so there is no
	//: window between the answer and the use of the thing it describes.
	if infoErr := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); infoErr != nil {
		closeErr := file.Close()
		//: LOCK_BACKEND_FAILED: a handle we cannot describe is not one to lock.
		return nil, foldAbandon(backendFailed("statHandle", path, infoErr), path, nil, closeErr)
	}
	//: a handle to the link itself. FILE_FLAG_OPEN_REPARSE_POINT stopped the
	//: redirection; this is what stops the lock living on the link.
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		closeErr := file.Close()
		attrs := strconv.FormatUint(uint64(info.FileAttributes), attrBase)
		//: LOCK_PATH_REDIRECTED, carrying the attribute word winnt.h names.
		return nil, foldAbandon(pathRedirected(path, kindReparsePoint, "0x"+attrs), path, nil, closeErr)
	}
	//: an ordinary file at the name this package derived.
	return file, nil
}

// classifyOpenFailure decides whether a failed open met an indirection or the
// medium.
//
// GetFileAttributesW reports the attributes of the NAME without following it,
// so it answers for a junction the open refused outright. It is diagnosis
// only: the open already failed, and nothing this function returns can turn
// that into success.
func classifyOpenFailure(path string, openErr error) error {
	namep, convErr := syscall.UTF16PtrFromString(path)
	//: a path with a NUL is not a path; report the open's own failure.
	if convErr != nil {
		//: LOCK_BACKEND_FAILED.
		return backendFailed("open", path, openErr)
	}
	attrs, attrErr := syscall.GetFileAttributes(namep)
	//: the name is an indirection the open would not traverse.
	if attrErr == nil && attrs&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		//: LOCK_PATH_REDIRECTED.
		return pathRedirected(path, kindReparsePoint, "0x"+strconv.FormatUint(uint64(attrs), attrBase))
	}
	//: LOCK_BACKEND_FAILED.
	return backendFailed("open", path, openErr)
}
