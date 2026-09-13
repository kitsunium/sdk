// Package pathchain resolves a path one component at a time and reports what
// each component IS, instead of only what the whole path finally names.
//
// # The question os.Stat cannot be asked
//
// os.Stat answers "what is at the end of this path". os.Lstat answers the same
// question without following the LAST component. Neither can say whether the
// path arrived where it did by going through somebody else's redirection, and
// that is the question a program asks when the path names a resource whose
// identity is a security property — a lock file, a state directory, a socket.
//
// O_NOFOLLOW has the same limit and it is a kernel limit, not a Go one: it
// governs the final component only. A link planted at a PARENT component is
// traversed by every open, whatever flags it carries.
//
// # What this package does instead
//
// [Resolve] walks the path from the filesystem root, holding an open directory
// handle at every step, and asks each component's own directory what that
// component is — lstat relative to a descriptor, never a fresh lookup of a
// path string. It returns one [StepValue] per component, in order, each
// carrying the mode of the directory it was found in.
//
// It does not refuse anything. A refusal needs a policy, a policy needs a
// domain, and this package has neither; what it has is the measurement a
// policy cannot be written without. The two callers this was built for want
// opposite verdicts on the same shape — /var/run being a symbolic link is a
// distribution's decision, /tmp/myapp being one may be an attack — and the
// difference is in StepValue.Container, not in the link.
//
// # What it rests on, and what that costs
//
// The directory handles are [os.Root], whose Unix implementation is
// openat(dirfd, name, O_NOFOLLOW|O_CLOEXEC|O_DIRECTORY) and whose Windows
// implementation passes O_NOFOLLOW_ANY — read in go1.27's src/os/root_unix.go
// and src/os/root_windows.go rather than assumed. That matters because
// syscall.Openat, the obvious primitive, exists in go1.27's syscall package
// for linux, aix and wasip1 ONLY: darwin reaches the kernel through libc
// trampolines that are internal to the standard library and does not even
// define SYS_OPENAT, so a hand-rolled walk would have served five kernels and
// silently skipped the sixth. os.Root is the standard library's own openat
// walk and it is available on every platform the SDK builds for.
//
// The cost of borrowing it is that os.Root resolves a symbolic link whose
// target stays inside the root and refuses one whose target leaves it, and
// neither behaviour is what a walk wants. So this package never asks os.Root
// to traverse an indirection: it detects one with Lstat, reads it with
// Readlink, and restarts the walk itself at the target. os.Root is used for
// exactly one thing — descending into a component already known to be a real
// directory.
package pathchain

import (
	"cmp"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxHops bounds how many indirections one resolution may follow.
//
// It is Linux's MAXSYMLINKS, and the bound exists for the same reason the
// kernel's does: two links pointing at each other resolve forever.
const maxHops int = 40

// current is the component every filesystem spells "this directory".
const current string = "."

// parent is the component every filesystem spells "the directory above".
const parent string = ".."

// Resolve walks path from the filesystem root and returns one [StepValue] per
// component, in the order they were traversed.
//
// A relative path is made absolute against the working directory first, so the
// chain always starts at a root and a caller never has to.
//
// # It stops where the path stops existing, and that is not an error
//
// A component that does not exist ends the walk and returns the steps taken so
// far with a nil error. That is deliberate: the caller this was built for
// audits a directory it is ABOUT to create, and "the last two components are
// not there yet" is the ordinary case rather than a fault. Every other
// failure — a permission denied, a component that is not a directory, an
// indirection loop — is returned as the *os.PathError the filesystem produced,
// unwrapped, so the caller's own error vocabulary can wrap it once.
//
// # The chain describes where resolution WENT
//
// A step's Path reflects the indirections already followed. Given
// /pub/app -> /srv/app and a walk of /pub/app/locks, the third step's Path is
// /srv/app/locks and not /pub/app/locks, because the third component genuinely
// lives in /srv/app and a caller deciding who could have replaced it must ask
// about that directory.
func Resolve(path string) (steps []StepValue, err error) {
	absolute, absErr := absoluteUncleaned(path)
	//: the working directory could not be read; nothing can be resolved
	//: against it.
	if absErr != nil {
		//: the filesystem's own error, unwrapped.
		return nil, absErr
	}
	return resolveAbs(absolute)
}

// absoluteUncleaned makes path absolute WITHOUT normalising its components.
//
// filepath.Abs is the obvious call and it is the wrong one: it Cleans, and
// Clean removes "link/.." LEXICALLY while the kernel applies the parent step
// AFTER following the link. Those are different directories whenever `link`
// does not point at its own parent's child — so a walk over the cleaned path
// would audit somewhere other than where the caller's open will land, which is
// the single failure this package exists to prevent.
//
// The one shape that cannot be kept verbatim is a Windows drive-relative path
// ("C:foo"), which has no expansion but the lexical one. It is named here
// rather than silently normalised with everything else.
func absoluteUncleaned(path string) (absolute string, err error) {
	//: already absolute: used exactly as written, "." and ".." included, both
	//: of which the walk applies as movements through handles it holds.
	if filepath.IsAbs(path) {
		//: verbatim.
		return path, nil
	}
	//: drive-relative on Windows — there is no descriptor-based expansion of
	//: "C:foo", so the lexical one is the only one available.
	if filepath.VolumeName(path) != "" {
		//: lexical, and only here.
		return filepath.Abs(path)
	}
	working, workingErr := os.Getwd()
	//: the working directory could not be read.
	if workingErr != nil {
		//: the filesystem's own error.
		return "", workingErr
	}
	//: concatenation rather than filepath.Join, which Cleans — see above.
	return working + string(filepath.Separator) + path, nil
}

// resolveAbs is [Resolve] once the path is known to be absolute.
func resolveAbs(abs string) (steps []StepValue, err error) {
	volume := filepath.VolumeName(abs)
	stack, openErr := openStack(volume + string(filepath.Separator))
	//: the filesystem root itself could not be opened.
	if openErr != nil {
		//: the filesystem's own error.
		return nil, openErr
	}
	defer func() {
		//: every directory handle this walk opened, given back. A refused
		//: close cannot have lost anything — nothing is written through these
		//: descriptors — but a filesystem that will not close one is saying
		//: something, and it is reported only when the walk itself had nothing
		//: to say, so a real resolution failure is never masked by it.
		err = cmp.Or(err, stack.close())
	}()
	walk := &walkState{stack: stack, volume: volume, pending: split(strings.TrimPrefix(abs, volume))}
	walk.collected = make([]StepValue, 0, len(walk.pending))
	//: one iteration per component still to resolve. An indirection puts its
	//: target's components back on the FRONT of the queue rather than
	//: recursing, so a chain of links costs no stack.
	for len(walk.pending) > 0 {
		name := walk.take()
		//: "." and ".." are movements through the chain, not components of
		//: it, so they are applied and never described.
		if name == current || name == parent {
			move(stack, name)
			//: on to the next component.
			continue
		}
		done, stepErr := walk.step(name, abs)
		//: the component could not be read at all.
		if stepErr != nil {
			//: the filesystem's own error.
			return nil, stepErr
		}
		//: the path stops existing here, or a non-directory ends it.
		if done {
			//: everything that does exist, described.
			return walk.collected, nil
		}
	}
	//: every component was described.
	return walk.collected, nil
}

// walkState is one resolution in progress: where it stands, what it has
// described, and what it has left to describe.
//
// It is a struct rather than four locals because the loop that drives it has
// to hand all four to the step that follows a link, and a five-argument helper
// returning three of them back is the shape this exists to avoid.
type walkState struct {
	// stack is the position: one open directory handle per level entered.
	stack *rootStack
	// collected is one StepValue per component described so far, in order.
	collected []StepValue
	// pending is what is left to resolve, front first. Following a link
	// prepends the target's components to it.
	pending []string
	// volume is the walk's starting volume, empty outside Windows. An
	// absolute link target with no volume of its own resolves against it.
	volume string
	// hops counts the indirections followed so far, bounded by maxHops.
	hops int
}

// take removes and returns the next component to resolve.
func (w *walkState) take() string {
	next := w.pending[0]
	w.pending = w.pending[1:]
	//: the caller has already checked that pending is not empty.
	return next
}

// step describes one component and either enters it or follows it.
//
// It reports done when the walk is over: the component does not exist, or it
// exists and cannot be entered with nothing left to resolve — a regular file
// as the last component, which is what a lock file is.
func (w *walkState) step(name, abs string) (done bool, err error) {
	described, describeErr := describe(w.stack, name)
	//: the component is not there: the path stops existing here, which is an
	//: answer rather than a fault — the caller may be auditing a directory it
	//: is about to create.
	if os.IsNotExist(describeErr) {
		//: the walk is over and nothing failed.
		return true, nil
	}
	//: the component could not be read at all.
	if describeErr != nil {
		//: the filesystem's own error.
		return false, describeErr
	}
	w.collected = append(w.collected, *described)
	//: an indirection is described and then walked; it is never descended
	//: into, because descending is what "following" means.
	if described.Indirect {
		//: the target's components go to the front of the queue.
		return false, w.follow(described.Target, abs)
	}
	return w.enter(name, *described)
}

// enter descends into a component already known not to be an indirection.
//
// described is the component as Lstat reported it a moment earlier, and it is
// what decides whether a failed descent is an ANSWER or a FAILURE. Treating
// every failure on the last component as a clean end reads two very different
// things as one: a regular file, which is a legitimate terminal and what a
// lock file is, and a directory that could not be opened — which is either a
// permission fault worth reporting or, worse, an entry swapped for an
// indirection between the Lstat and this call, where reporting success would
// hand the caller a chain describing a component that no longer exists.
func (w *walkState) enter(name string, described StepValue) (done bool, err error) {
	enterErr := w.stack.push(name)
	//: entered; the next component will be looked up in the right directory.
	if enterErr == nil {
		//: keep going.
		return false, nil
	}
	//: a terminal that Lstat did not call a directory ends the walk cleanly.
	//: Describing it was the point, and there is nothing below it to look up.
	if !described.Mode.IsDir() && len(w.pending) == 0 {
		//: the walk is over and nothing failed.
		return true, nil
	}
	//: everything else: a directory that would not open, or a non-directory
	//: with components still to resolve. Continuing would describe them
	//: against the directory ABOVE, which is worse than refusing.
	return false, enterErr
}

// follow re-queues an indirection's target, bounded by [maxHops].
func (w *walkState) follow(target, abs string) error {
	w.hops++
	//: two links pointing at each other resolve forever.
	if w.hops > maxHops {
		//: the refusal is the KERNEL's own, asked for by handing it the whole
		//: path — so the errno is whatever this platform spells the condition
		//: with (ELOOP on Linux, EMLINK on FreeBSD, EFTYPE on NetBSD) and no
		//: table here has to be right on the seventh.
		return exhausted(abs)
	}
	restart, restartErr := redirect(w.stack, target, w.volume)
	//: the target's own root could not be opened.
	if restartErr != nil {
		//: the filesystem's own error.
		return restartErr
	}
	w.pending = append(restart, w.pending...)
	//: the target's components are next.
	return nil
}

// move applies a component that changes position without being part of the
// chain.
func move(stack *rootStack, name string) {
	//: staying put moves nothing.
	if name == current {
		//: nothing to do.
		return
	}
	//: ascending leaves the level the walk entered; at the root it is a no-op,
	//: exactly as the kernel treats "/..".
	stack.pop()
}

// describe reads one component without traversing it.
func describe(stack *rootStack, name string) (step *StepValue, err error) {
	root := stack.top()
	info, statErr := root.Lstat(name)
	//: missing, unreadable, or anything else the filesystem objects to. The
	//: caller separates "not there" from "could not be read"; both arrive as
	//: the error the filesystem produced.
	if statErr != nil {
		//: the filesystem's own error.
		return nil, statErr
	}
	container, containerErr := root.Stat(current)
	//: the directory we are standing in could not describe itself.
	if containerErr != nil {
		//: the filesystem's own error.
		return nil, containerErr
	}
	built := StepValue{
		Path:      filepath.Join(stack.path(), name),
		Name:      name,
		Container: container.Mode(),
		Mode:      info.Mode(),
		Indirect:  indirect(info.Mode()),
	}
	//: an ordinary component says everything about itself in its mode.
	if !built.Indirect {
		//: described.
		return &built, nil
	}
	target, linkErr := root.Readlink(name)
	//: an indirection whose target cannot be read cannot be FOLLOWED either,
	//: and the caller does follow it. Continuing with an empty target would
	//: resolve every remaining component against the link's own container and
	//: return a chain describing a different path from the one asked about.
	if linkErr != nil {
		//: the filesystem's own error.
		return nil, linkErr
	}
	//: the stored target, unresolved.
	built.Target = target
	//: described, and the caller must not descend into it.
	return &built, nil
}

// indirect reports whether a mode describes a component that redirects
// resolution.
//
// ModeSymlink covers a Unix symbolic link and, on Windows, both indirections
// that matter: go1.27's src/os/types_windows.go maps IO_REPARSE_TAG_SYMLINK
// and IO_REPARSE_TAG_MOUNT_POINT — a directory junction — onto it. Every other
// reparse tag becomes ModeIrregular, which is also treated as an indirection
// here rather than as an ordinary file, because a component the standard
// library declines to classify is not one to walk through silently.
func indirect(mode fs.FileMode) bool {
	//: either spelling of "this component is not what it names".
	return mode&(fs.ModeSymlink|fs.ModeIrregular) != 0
}

// redirect turns an indirection's target into the components to resolve next,
// resetting the stack to the target's own root when the target is absolute.
func redirect(stack *rootStack, target, volume string) (pending []string, err error) {
	//: a relative target continues from the directory the link lives in, which
	//: is where the stack already stands.
	if !filepath.IsAbs(target) {
		//: the target's components, in order.
		return split(target), nil
	}
	//: an absolute target restarts resolution at a root, so the stack unwinds
	//: to one. The link's own volume wins over the walk's, because a Windows
	//: junction may cross drives.
	resetErr := stack.reset(rootOf(target, volume))
	//: the target's root could not be opened.
	if resetErr != nil {
		//: the filesystem's own error.
		return nil, resetErr
	}
	//: the target's components, below its root.
	return split(strings.TrimPrefix(target, filepath.VolumeName(target))), nil
}

// rootOf returns the filesystem root a target resolves against.
func rootOf(target, fallback string) string {
	//: an absolute target carrying no volume of its own stays on the walk's,
	//: which outside Windows is the empty string and therefore always the
	//: filesystem root.
	return cmp.Or(filepath.VolumeName(target), fallback) + string(filepath.Separator)
}

// split breaks a path into its non-empty components.
func split(path string) []string {
	parts := strings.FieldsFunc(path, isSeparator)
	//: FieldsFunc drops empty fields, so repeated and trailing separators need
	//: no second pass — and a path that is nothing but separators yields an
	//: EMPTY slice rather than one empty component, which is the answer the
	//: walk wants: "/" has no components below its root.
	if len(parts) == 0 {
		//: an explicit empty slice, so no caller has to decide whether a nil
		//: means "no components" or "not computed".
		return []string{}
	}
	return parts
}

// isSeparator reports whether r separates path components on this platform.
func isSeparator(r rune) bool {
	//: filepath.Separator is '/' everywhere but Windows, where '/' is also
	//: accepted by every API, so both are separators there and only one is
	//: reachable anywhere else.
	return r == filepath.Separator || r == '/'
}

// exhausted reports a path that could not be resolved within [maxHops].
//
// It deliberately does not invent an errno. It hands the whole path to os.Stat
// and returns what the KERNEL says about it, which for an indirection loop is
// the platform's own spelling of the condition — ELOOP on Linux and Darwin,
// EMLINK on FreeBSD and DragonFly, EFTYPE on NetBSD. A constant chosen here
// would be one of those three and therefore wrong on at least two platforms,
// and syscall does not define any of them on every GOOS this package compiles
// for.
func exhausted(path string) error {
	_, statErr := os.Stat(path)
	//: the ordinary outcome is the kernel's own refusal of the same path for
	//: the same reason. The fallback is reachable only if the path changed
	//: underneath us between the walk and the stat, and it is still reported
	//: as an unusable path rather than as success.
	return cmp.Or(statErr, error(&os.PathError{Op: "resolve", Path: path, Err: fs.ErrInvalid}))
}
