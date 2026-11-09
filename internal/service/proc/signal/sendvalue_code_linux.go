//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package signal

// siCodeOffset is si_code, the third int: si_signo, si_errno, si_code.
const siCodeOffset int = 8
