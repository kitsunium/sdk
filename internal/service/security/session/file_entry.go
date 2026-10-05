package session

import (
	"cmp"
	"errors"
	"io"
	"io/fs"
	"os"
)

// kindSymlink names an indirection found at a name in the store directory.
const kindSymlink string = "symlink"

// kindNotRegular names an entry that is neither a regular file nor a link — a
// directory, a FIFO, a socket. Opening one is either pointless or, for a FIFO,
// a wait that would hold the store-wide lock.
const kindNotRegular string = "not-regular"

// kindReplaced names a name that, once compared with the handle its open
// produced, did not lead to the file that handle holds.
const kindReplaced string = "replaced"

// openEntry opens name in the held directory and proves the handle is the
// regular file the name leads to — never what a link planted at the name
// points to.
//
// It has three outcomes. A handle the caller owns, with found empty. No handle
// and found naming what stood at the name instead — [kindSymlink],
// [kindNotRegular] or [kindReplaced] — because finding one is a VERDICT the
// caller turns into its own refusal (a record and the lock file are refused
// differently); err is then nil, or the failure to close a handle the proof
// refused, which rides along and never replaces the verdict. Or, with found
// empty, the filesystem's own failure, unwrapped, so each caller wraps it once:
// fs.ErrNotExist is how an absent name reads when flag does not create it.
//
// Callers read found BEFORE err, for exactly that reason.
func openEntry(root *os.Root, name string, flag int, perm fs.FileMode) (file *os.File, found string, err error) {
	before, present, lookErr := lookAt(root, name)
	//: the name could not even be looked at.
	if lookErr != nil {
		//: the filesystem's own failure.
		return nil, "", lookErr
	}
	//: something is already there, and it is not a regular file: refused
	//: before any open, so nothing is followed or waited on. An absent name
	//: has no kind and goes on to the open.
	if kind, wrong := entryKind(before); wrong {
		//: the verdict, for the caller to spell.
		return nil, kind, nil
	}
	opened, openErr := root.OpenFile(name, flag, perm)
	//: the open itself failed — including the one way os.Root refuses a link
	//: swapped in since the look, which is a link that leaves the directory.
	if openErr != nil {
		//: an indirection is a verdict rather than a failure.
		if isLinkNow(root, name) {
			//: no error; the caller reads the verdict.
			return nil, kindSymlink, nil
		}
		//: the medium, unwrapped.
		return nil, "", openErr
	}
	kind, proveErr := sameEntry(root, name, opened, before, present)
	//: the handle is the regular file the name leads to.
	if proveErr == nil && kind == "" {
		//: the caller owns it.
		return opened, "", nil
	}
	closeErr := opened.Close()
	//: the proof could not be taken: a failure, and a failed close is the
	//: answer only when there was nothing better to say.
	if proveErr != nil {
		//: the filesystem's own failure.
		return nil, "", cmp.Or(proveErr, closeErr)
	}
	//: the proof refused the handle: the verdict, with the close failure — if
	//: there was one — riding along.
	return nil, kind, closeErr
}

// readEntry reads the whole regular file named name in the held directory,
// refusing an indirection exactly as [openEntry] does.
func readEntry(root *os.Root, name string) (raw []byte, found string, err error) {
	file, found, openErr := openEntry(root, name, os.O_RDONLY, 0)
	//: absent, unreadable, or not a file this store wrote.
	if openErr != nil || found != "" {
		//: the verdict, unchanged.
		return nil, found, openErr
	}
	raw, readErr := io.ReadAll(file)
	closeErr := file.Close()
	//: a read failure wins; a close failure on a read-only handle is still
	//: CHECKED rather than discarded, and reported only on its own.
	return raw, "", cmp.Or(readErr, closeErr)
}

// lookAt reports what stands at name, without following it.
func lookAt(root *os.Root, name string) (info fs.FileInfo, present bool, err error) {
	info, statErr := root.Lstat(name)
	//: absent is an answer, not a fault: a record that was never written, a
	//: lock file about to be created.
	if errors.Is(statErr, fs.ErrNotExist) {
		//: nothing there.
		return nil, false, nil
	}
	//: anything else is the filesystem failing to answer.
	if statErr != nil {
		//: the filesystem's own failure.
		return nil, false, statErr
	}
	//: something is there, described rather than traversed.
	return info, true, nil
}

// entryKind names what is wrong with an entry for this store's purposes, and
// reports whether anything is. A nil info — nothing there — is not wrong.
func entryKind(info fs.FileInfo) (kind string, wrong bool) {
	//: nothing to judge.
	if info == nil {
		//: absent is not an indirection.
		return "", false
	}
	//: a link, dangling or not.
	if info.Mode()&fs.ModeSymlink != 0 {
		//: the case this file exists for.
		return kindSymlink, true
	}
	//: a directory, a FIFO, a socket, a device.
	if !info.Mode().IsRegular() {
		//: not a file this store would write.
		return kindNotRegular, true
	}
	//: a regular file.
	return "", false
}

// sameEntry proves that handle holds the regular file name leads to.
//
// When the name was there before the open, the handle must be THAT file: the
// look already established what the name was, and a swap since then is what
// this exists to catch. When it was absent — the open created it — the name is
// looked at again, and must now be a regular file and the same one.
func sameEntry(root *os.Root, name string, handle *os.File, before fs.FileInfo, present bool) (found string, err error) {
	held, statErr := handle.Stat()
	//: a handle that cannot be described cannot be vouched for.
	if statErr != nil {
		//: the filesystem's own failure.
		return "", statErr
	}
	//: the open landed on something that is not a regular file.
	if !held.Mode().IsRegular() {
		//: a name swapped for a directory or a FIFO since the look.
		return kindNotRegular, nil
	}
	want := before
	//: created by this open: what the name is NOW is what it has to match.
	if !present {
		now, nowPresent, nowErr := lookAt(root, name)
		//: the name could not be looked at again.
		if nowErr != nil {
			//: the filesystem's own failure.
			return "", nowErr
		}
		//: gone again already, or no longer a regular file.
		if kind, wrong := entryKind(now); !nowPresent || wrong {
			//: the name does not lead to what was opened.
			return cmp.Or(kind, kindReplaced), nil
		}
		want = now
	}
	//: the file the name led to and the file the handle holds must be one.
	if !os.SameFile(held, want) {
		//: swapped between the look and the open.
		return kindReplaced, nil
	}
	//: proven.
	return "", nil
}

// isLinkNow reports whether name is a link now — asked after an open of it
// failed.
//
// It is DIAGNOSIS, never the decision: the open already failed, so nothing
// this returns can turn the failure into a success; it only says which of two
// things the failure was. A link that leaves the directory is the one shape
// os.Root refuses at the open instead of following.
func isLinkNow(root *os.Root, name string) bool {
	info, present, lookErr := lookAt(root, name)
	kind, _ := entryKind(info)
	//: a link, swapped in since the look; anything else is the medium.
	return lookErr == nil && present && kind == kindSymlink
}
