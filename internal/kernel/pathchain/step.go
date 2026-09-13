// Package pathchain — one COMPONENT of a resolved path, described rather than
// traversed, with the mode of the directory it was found in beside it.
package pathchain

import "io/fs"

// StepValue describes one component of a resolved path: what that component
// IS, and who could have put it there.
//
// The second half is the reason this type exists. Knowing that a component is
// a symbolic link answers almost nothing on its own, because the answer is
// "yes" for /tmp on macOS, for /var/run on most Linux distributions, and for
// C:\Users\All Users on Windows — all of them legitimate, all of them planted
// by the operating system's installer. What separates those from an attack is
// not the link, it is the DIRECTORY THE LINK LIVES IN: a link in a directory
// only root can write was put there by root, and a link in a world-writable
// directory was put there by anybody at all.
//
// So every step carries Container, the mode of the directory the component was
// looked up in, read by fstat on a handle this package held open rather than
// by a second path lookup that could resolve somewhere else.
type StepValue struct {
	// Path is the path as resolved up to and including this component. It
	// reflects the indirections already followed, so it is where the component
	// actually lives rather than what the caller typed.
	Path string
	// Name is this component alone, with no separator.
	Name string
	// Container is the mode of the directory this component was looked up in.
	//
	// On Unix its permission bits answer "who could have created this entry".
	// On Windows os.Stat synthesises the bits from one read-only attribute, so
	// they answer nothing there and a caller needing that answer must get it
	// from the platform's own access-control model. This package reports what
	// it read and takes no position on what it means.
	Container fs.FileMode
	// Mode is the mode of the component itself, as lstat reports it: an
	// indirection is described here, never traversed to describe its target.
	Mode fs.FileMode
	// Target is where the indirection points, exactly as the filesystem stores
	// it — relative or absolute, unresolved. It is empty unless Indirect.
	Target string
	// Indirect records that this component is a symbolic link on Unix or a
	// reparse point on Windows, so resolving the path CONTINUED somewhere the
	// component's own name does not say.
	Indirect bool
}
