//go:build linux

package exec

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/childwait"
)

// reclaimChild claims pid's exit status and wraps it in Start's handle, once
// /proc says it is this process's child — before the claim and after it: a
// child's pid is not reused until this process reaps it, so the second read
// proves the claim holds the child the first one saw.
func reclaimChild(pid int) (coreproc.Process, error) {
	self := os.Getpid()
	//: not this process's child, or gone.
	if !childOf(pid, self) {
		//: ADOPTION_REFUSED naming the pid.
		return nil, wrapAdoptionRefused(nil, errs.Int("pid", pid), errs.String("why", "not a child of this process"))
	}
	proc, claim, err := childwait.Spawn(func() (*os.Process, error) { return os.FindProcess(pid) })
	//: no handle on it.
	if err != nil {
		//: HANDLE_UNAVAILABLE.
		return nil, wrapHandleUnavailable(err, errs.Int("pid", pid))
	}
	//: reaped meanwhile by someone else: the claim holds nothing.
	if !childOf(pid, self) {
		claim.Release()
		swallowErr(proc.Release())
		//: ADOPTION_REFUSED naming the pid.
		return nil, wrapAdoptionRefused(nil, errs.Int("pid", pid), errs.String("why", "not a child of this process"))
	}
	pgid, gerr := syscall.Getpgid(pid)
	//: Start's handle; the group is the child's when it leads one.
	return newHandle(proc, claim, gerr == nil && pgid == pid, &stdioState{}), nil
}

// childOf reports whether /proc/<pid>/stat names parent as pid's parent: its
// fourth field, after the command name's closing parenthesis.
func childOf(pid, parent int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	//: gone.
	if err != nil {
		//: no.
		return false
	}
	end := strings.LastIndexByte(string(raw), ')')
	//: no command name.
	if end < 0 {
		//: no.
		return false
	}
	fields := strings.Fields(string(raw[end+1:]))
	//: the state, then the parent.
	if len(fields) < 2 {
		//: no.
		return false
	}
	ppid, err := strconv.Atoi(fields[1])
	//: the parent named.
	return err == nil && ppid == parent
}
