//go:build unix

// Package ipc — the socket directory on Unix: created 0700, refused when
// another account could write to it, when it is a link, or when another
// account could steer the path to it (chain_unix.go).
package ipc

import (
	"errors"
	"os"
	"syscall"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// groupOrWorldWritable are the mode bits that let another account plant an
// entry in the directory.
const groupOrWorldWritable os.FileMode = 0o022

// prepareDir creates dir 0700 when it is missing, then checks it. The path
// above dir is audited BEFORE anything is created, because Mkdir follows a
// link at a parent and would otherwise create the directory inside whatever
// tree the link names; and again after, with the rest of [checkDir], because a
// parent missing at the first audit may have been created by somebody else
// in between.
func prepareDir(dir string) error {
	if err := checkChain(dir); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the directory cannot be created"),
			errs.String("path", dir), errs.String("cause", err.Error()))
	}
	return checkDir(dir)
}

// checkDir refuses a socket directory another account could write to, read
// through, replace or reach through a path it steers: every component above
// it first ([checkChain], PATH_UNSAFE), then its own entry ([checkEntry],
// DIRECTORY_UNSAFE), then the directory holding it ([checkHolder]).
func checkDir(dir string) error {
	if err := checkChain(dir); err != nil {
		return err
	}
	if err := checkEntry(dir); err != nil {
		return err
	}
	return checkHolder(dir)
}

// checkEntry refuses a socket directory whose own entry another account could
// write to or read through: it must be a real directory — not a link —, owned
// by this process's user, and writable by nobody else. Group or world search
// permission is allowed: that is how a deployment admits an on-call group,
// and without write permission nobody can plant an entry there.
func checkEntry(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case err != nil:
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the directory cannot be read"),
			errs.String("path", dir), errs.String("cause", err.Error()))
	case info.Mode()&os.ModeSymlink != 0:
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the directory is a link"), errs.String("path", dir))
	case !info.IsDir():
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "not a directory"), errs.String("path", dir))
	case info.Mode().Perm()&groupOrWorldWritable != 0:
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "writable by its group or by anyone"),
			errs.String("path", dir), errs.String("mode", info.Mode().Perm().String()))
	}
	if uid, known := ownerOf(info); known && uid != os.Geteuid() {
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "owned by another account"),
			errs.String("path", dir), errs.Int("uid", uid))
	}
	return nil
}

// ownerOf is the UID owning the file info describes; unknown for a nil info
// or one the platform does not describe with a Stat_t.
func ownerOf(info os.FileInfo) (uid int, known bool) {
	if info == nil {
		return -1, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, false
	}
	return int(st.Uid), true
}
