//go:build unix

package rlimit

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// keepRaisedNoFile reads the open-file limit in force and sets it again through
// syscall.Setrlimit, whose RLIMIT_NOFILE case clears the original soft limit the
// runtime saved at start (syscall/rlimit.go): forkExec then restores nothing,
// and every direct spawn inherits the raised limit. The value is unchanged, so
// the call never needs privilege.
func keepRaisedNoFile() (kept coreproc.LimitValue, err error) {
	var lim syscall.Rlimit
	//: the limit in force — the runtime's raise, or what the program set since.
	if gErr := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); gErr != nil {
		//: nothing to keep without a reading.
		return coreproc.LimitValue{}, keepRaisedFailed(gErr, "getrlimit")
	}
	//: the same value, set by the program: the runtime forgets its saved original.
	if sErr := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); sErr != nil {
		//: the restore stays in force.
		return coreproc.LimitValue{}, keepRaisedFailed(sErr, syscallSetrlimit)
	}
	//: the limit every child now inherits.
	return limitValueOf(lim.Cur, lim.Max), nil
}

// limitValueOf turns an Rlimit's Cur and Max — int64 on FreeBSD and DragonFly,
// uint64 elsewhere — into the port's pair. The int64 RLIM_INFINITY (-1) lands
// on LimitInfinity, the inverse of rlim.Make.
func limitValueOf[T int64 | uint64](soft, hard T) coreproc.LimitValue {
	//: a value-preserving widening for every real limit.
	return coreproc.LimitValue{Soft: uint64(soft), Hard: uint64(hard)}
}

// keepRaisedFailed wraps a getrlimit/setrlimit failure of KeepRaisedNoFile as
// the central RLIMIT_FAILED sentinel, naming the syscall that failed.
func keepRaisedFailed(cause error, syscallName string) error {
	//: re-state the RLIMIT_FAILED sentinel's fields without re-Defining the code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeRlimitFailed,
		Reason:   "RLIMIT_FAILED",
		Public:   "Could not apply the resource limit",
		Private:  "service/proc/rlimit.KeepRaisedNoFile: " + syscallName + "(RLIMIT_NOFILE) failed",
		ExitCode: exitOSErr,
	}, errs.String("resource", coreproc.ResourceNoFile.String()), errs.String("syscall", syscallName))
}
