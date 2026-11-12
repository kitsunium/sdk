//go:build linux && !(ppc64 || ppc64le || mips || mipsle || mips64 || mips64le)

package loop

// The generic ioctl encoding (include/uapi/asm-generic/ioctl.h): the
// direction in the top two bits.
const (
	iocDirShift uintptr = 30
	iocWrite    uintptr = 1
	iocRead     uintptr = 2
)
