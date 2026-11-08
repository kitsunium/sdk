//go:build linux && (mips || mipsle || mips64 || mips64le)

package signal

// siCodeOffset is si_code, the second int: MIPS swaps si_code and si_errno
// (__ARCH_HAS_SWAPPED_SIGINFO).
const siCodeOffset int = 4
