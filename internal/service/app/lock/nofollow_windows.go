//go:build windows

package lock

import (
	"os"
	"strconv"
	"syscall"
)

// openReparsePoint is FILE_FLAG_OPEN_REPARSE_POINT (winbase.h), passed in the
// high 12 bits of the flag word that syscall.Open forwards to CreateFileW.
const openReparsePoint int = syscall.FILE_FLAG_OPEN_REPARSE_POINT

// kindReparsePoint names this platform's indirection in the refusal's fields.
// It covers a symbolic link and a junction alike: both are reparse points, and
// both redirect the open.
const kindReparsePoint string = "reparse_point"

// hardenedOpen reports that this GOOS's [openLockFile] refuses an indirection
// planted at the lock path — FILE_FLAG_OPEN_REPARSE_POINT and the handle check,
// here. It is true exactly where internal/kernel/fs/flock.Native is: a platform
// gains the lock and its hardening together, or neither (backend.go).
const hardenedOpen bool = true

// attrBase is the radix the refusal renders the attribute word in. Hexadecimal
// is what winnt.h spells FILE_ATTRIBUTE_* in, so a reader can compare the
// reported value against the header without converting it first.
const attrBase int = 16

// openLockFile opens the lock file, refusing a reparse point planted at path.
//
// It reports [corelock.LockPathRedirected] for an indirection and
// [corelock.LockBackendFailed] for anything else, which is the same pair the
// Unix half reports — the sentinel is the contract, the mechanism is not.
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
