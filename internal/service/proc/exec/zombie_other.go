//go:build unix && !darwin

package exec

// zombiesRefused is false: every other kernel counts a zombie when it signals
// a group, so a group of zombies is signalled and an emptied one is ESRCH.
const zombiesRefused bool = false

// leaderIsZombie answers false: only darwin refuses a group of zombies with
// EPERM, so no other kernel consults it — and getpgid(2) still finds a zombie
// on Linux, where reading it would call a dead leader alive. solaris, illumos
// and aix have no getpgid in the syscall package at all.
func leaderIsZombie(_ int) bool { return false }
