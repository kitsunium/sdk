//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package exec

// sysCloseRange is __NR_close_range, 436 on every Linux architecture of the
// unified syscall table (include/uapi/asm-generic/unistd.h and the x86 and
// arm tables alike). The mips ABIs number it apart and walk instead.
const sysCloseRange uintptr = 436
