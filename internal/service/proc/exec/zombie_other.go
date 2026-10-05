//go:build unix && !darwin

package exec

// leaderIsZombie answers false: only darwin refuses a group of zombies with
// EPERM, so no other kernel consults it — and getpgid(2) still finds a zombie
// on Linux, where reading it would call a dead leader alive. solaris, illumos
// and aix have no getpgid in the syscall package at all.
func leaderIsZombie(int) bool { return false }
