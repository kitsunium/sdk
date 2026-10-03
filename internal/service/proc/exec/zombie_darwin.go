// Package exec — darwin's word on whether this process's unreaped child has
// died: the probe zombieGroupRefused needs because darwin's kill(2) answers a
// group of zombies with EPERM.
package exec

import (
	"errors"
	"syscall"
)

// leaderIsZombie reports whether pid — a child of this process that it has
// not reaped — has exited. XNU's getpgid(2) looks the pid up with proc_find,
// which skips a process once it has exited, so it answers ESRCH for a zombie;
// a live child answers with its group. The pid cannot have been reused while
// it is unreaped, since only the reap frees it, so ESRCH here means "dead",
// never "somebody else's". Measured on darwin 25.6: kill(-pgid) = EPERM,
// kill(pid, 0) = success (POSIX lets a zombie be signalled) and getpgid(pid) =
// ESRCH for the same zombie leader.
func leaderIsZombie(pid int) bool {
	_, err := syscall.Getpgid(pid)
	//: ESRCH is the zombie (or the already collected); anything else is alive.
	return errors.Is(err, syscall.ESRCH)
}
