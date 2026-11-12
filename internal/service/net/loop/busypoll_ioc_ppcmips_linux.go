//go:build linux && (ppc64 || ppc64le || mips || mipsle || mips64 || mips64le)

package loop

// powerpc's and mips' ioctl encoding (arch/*/include/uapi/asm/ioctl.h): the
// direction in the top three bits, write 4.
const (
	iocDirShift uintptr = 29
	iocWrite    uintptr = 4
	iocRead     uintptr = 2
)
