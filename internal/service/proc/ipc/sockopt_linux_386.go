package ipc

// getsockoptTrap is getsockopt(2)'s own system call on i386, 365 since Linux
// 4.3 (arch/x86/entry/syscalls/syscall_32.tbl): the syscall package reaches
// it through socketcall only, and SO_PEERGROUPS (4.13) is newer than both.
const getsockoptTrap uintptr = 365
