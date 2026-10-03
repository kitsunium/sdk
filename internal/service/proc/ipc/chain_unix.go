//go:build unix

// Package ipc — the components ABOVE the socket's directory, and who could
// steer them: ADR 0083's rule, applied where the directory is the whole of the
// access control (ADR 0148).
//
// # One Lstat sees one component; every lookup crosses all of them
//
// [checkEntry] judges the socket's directory by its own entry: not a link, a
// directory, this account's, written by nobody else. It cannot see a PARENT,
// and every lookup this package makes — the Mkdir that creates the directory,
// the bind that creates the socket, the connect that reaches it — follows a
// link planted at one. Measured on the code before this file, with a link
// planted in a 0777|sticky directory, which is what /tmp is:
//
//	Config.Path     /tmp/ipc…/pub/app/run/d.sock    pub dtrwxrwxrwx, app -> …/elsewhere
//	NewListener     accepted: the socket was bound at …/elsewhere/run/d.sock
//	Dial            accepted
//	app re-planted  -> …/trap, where another listener waited
//	Dial            accepted: the trap's listener read the client's first line
//
// The planter owns its link, so the sticky bit lets it replace that link at
// will: it decides where the directory is created, and later which socket a
// client reaches. Across accounts it also owns the directory the socket's
// directory was created in, and on macOS an inheritable access-control entry
// it puts there is inherited by the 0700 directory Listen creates and by the
// 0600 socket bound inside — observed with ls -le — which no mode check sees.
// Outside Linux the peer is not verified, so that entry is a way in.
//
// # The rule: who could have created a component, and who can replace it
//
// A link at a parent is not evidence on its own — /tmp -> private/tmp and
// /var -> private/var on macOS, /var/run -> /run on most Linux distributions —
// and what separates those from an attack is the directory the component
// lives in, exactly as internal/service/lock reasons (ADR 0083). So every
// component above the socket's directory is judged by the directory holding
// it, and only when ANYBODY can write that directory:
//
//   - an indirection is refused ([kindIndirection]): anybody could have
//     planted it. The sticky bit does not exempt it — sticky governs UNLINKING
//     an entry that exists, and planting creates one at a name nobody had
//     taken. This is lock's rule, unchanged.
//   - a component owned by neither this process's user nor root is refused
//     ([kindForeign]): anybody could have created it, and whoever did decides
//     everything below it, down to the entries a directory made there
//     inherits. lock has no such rule because a lock is shared between
//     accounts on purpose; a private socket's directory must be this
//     account's, and its gate is that nobody else reaches it.
//   - without the sticky bit, any component is refused ([kindReplaceable]):
//     anybody can rename it away and put their own in its place. The socket's
//     directory itself is held to the same rule by [checkHolder].
//
// A directory writable by its GROUP is not judged: a directory shared with a
// group is a deliberate arrangement, as lock accepts it.
//
// # What it does not see
//
// A component this rule accepts can be replaced only by this account, by
// root, by the owner of the directory holding it or by that directory's group
// — accounts the deployment chose by placing the socket there. A socket under a tree another
// account owns therefore trusts that account, by the deployment's own choice,
// and nothing here refuses it. The audit runs at Listen — before the directory
// is created and again after — and at every Dial; on Linux the peer's
// credentials are the gate that holds between them.
package ipc

import (
	"os"
	"path/filepath"

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
		return errs.Wrap(DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the path to it cannot be resolved"),
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
		return errs.Wrap(DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the directory holding it cannot be read"),
			errs.String("path", dir), errs.String("cause", err.Error()))
	}
	if mode := holder.Mode(); mode&worldWritable != 0 && mode&os.ModeSticky == 0 {
		return errs.Wrap(PathUnsafe, errs.WrapParams{}, errs.String("path", dir), errs.String("dir", dir),
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
	return errs.Wrap(PathUnsafe, errs.WrapParams{}, fields...)
}
