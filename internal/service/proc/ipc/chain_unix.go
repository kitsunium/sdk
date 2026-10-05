//go:build unix

package ipc

import (
	"os"
	"path/filepath"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/fs/pathchain"
)

// worldWritable is the bit that lets any account create, rename or remove an
// entry in a directory.
const worldWritable os.FileMode = 0o002

// rootUID is the one account besides this process's user a component may
// belong to: root can replace anything anyway.
const rootUID int = 0

// The kind field of a PATH_UNSAFE refusal: how another account could steer
// the component it names.
const (
	// kindIndirection is a link held by a directory anybody can write.
	kindIndirection string = "indirection"
	// kindForeign is a component another account owns, held by a directory
	// anybody can write.
	kindForeign string = "foreign"
	// kindReplaceable is a component held by a directory anybody can write
	// and that has no sticky bit.
	kindReplaceable string = "replaceable"
)

// checkChain refuses the socket directory dir when another account could steer
// a component of the path ABOVE it. The socket directory's own entry is not
// judged here — [checkEntry] holds it to stricter rules — so the walk is over
// its parent, and dir need not exist yet: [prepareDir] runs this before it
// creates anything, so nothing is ever created inside a steered tree.
func checkChain(dir string) error {
	steps, err := pathchain.Resolve(filepath.Dir(dir))
	if err != nil {
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the path to it cannot be resolved"),
			errs.String("path", dir), errs.String("cause", err.Error()))
	}
	self := os.Geteuid()
	for i := range steps {
		if kind, steered := steerable(&steps[i], self); steered {
			return pathUnsafe(dir, &steps[i], kind)
		}
	}
	return nil
}

// checkHolder refuses a socket directory anybody could replace: one held by a
// directory anybody can write that has no sticky bit. [checkChain] judged the
// holder's own path first, so the lookup lands where that audit did.
func checkHolder(dir string) error {
	holder, err := os.Stat(filepath.Dir(dir))
	if err != nil {
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the directory holding it cannot be read"),
			errs.String("path", dir), errs.String("cause", err.Error()))
	}
	if mode := holder.Mode(); mode&worldWritable != 0 && mode&os.ModeSticky == 0 {
		return errs.Wrap(coreipc.PathUnsafe, errs.WrapParams{}, errs.String("path", dir), errs.String("dir", dir),
			errs.String("kind", kindReplaceable), errs.String("container", mode.String()))
	}
	return nil
}

// steerable says whether another account could steer the path at step and,
// when one could, how — the kind a refusal names. steered is false when no
// account the deployment did not choose can.
func steerable(step *pathchain.StepValue, self int) (kind string, steered bool) {
	switch {
	case step.Container&worldWritable == 0:
		// Only root, the holder's owner and its group write the holder.
		return "", false
	case step.Indirect:
		return kindIndirection, true
	case !ours(step.Info, self):
		return kindForeign, true
	case step.Container&os.ModeSticky == 0:
		return kindReplaceable, true
	}
	return "", false
}

// ours reports whether info belongs to this process's user or to root. An
// owner that cannot be read belongs to nobody trusted — a gate does not guess
// — though on Unix it always can be read.
func ours(info os.FileInfo, self int) bool {
	uid, known := ownerOf(info)
	return known && (uid == self || uid == rootUID)
}

// pathUnsafe is the PATH_UNSAFE refusal for step: the component that may be
// steered and how, the socket directory the caller configured, the mode of the
// directory holding the component, where an indirection leads and who owns
// what was found.
func pathUnsafe(dir string, step *pathchain.StepValue, kind string) error {
	fields := []errs.FieldValue{
		errs.String("path", step.Path),
		errs.String("dir", dir),
		errs.String("kind", kind),
		errs.String("container", step.Container.String()),
	}
	if step.Indirect {
		fields = append(fields, errs.String("target", step.Target))
	}
	if uid, known := ownerOf(step.Info); known {
		fields = append(fields, errs.Int("uid", uid))
	}
	return errs.Wrap(coreipc.PathUnsafe, errs.WrapParams{}, fields...)
}
