package proc

// AttachValue bounds the process a PID file may name when the file is not
// trustworthy — reached through a symbolic link another user could have made,
// or kept in a directory, or written to a file, that another user could
// change: the process must then lie in the cgroup v2 subtree Cgroup and run as
// one of UIDs. A bound that is given is checked whatever the file; the zero
// value bounds nothing, and a file that is not trustworthy is then refused.
type AttachValue struct {
	// Cgroup is a cgroup v2 path as /proc/<pid>/cgroup names it —
	// "/system.slice/web.service" —: the process must be in it or below it. Empty
	// checks no cgroup.
	Cgroup string
	// UIDs are the users the process may run as: its real and its effective uid
	// must both be one of them. Empty checks no user.
	UIDs []int
}
