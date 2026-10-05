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
//
// Package pathchain — the walk's POSITION: one open directory handle per
// component already entered, so every question is asked of a descriptor this
// process holds rather than of a path string that could resolve elsewhere.
//
// Package pathchain — one COMPONENT of a resolved path, described rather than
// traversed, with the mode of the directory it was found in beside it.
package pathchain
