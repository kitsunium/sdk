//go:build freebsd

package exec

// sysCloseRange is SYS_close_range, 575 in FreeBSD's sys/sys/syscall.h (13.0
// and later). A kernel without it answers ENOSYS, and the seal walks /dev/fd.
const sysCloseRange uintptr = 575
