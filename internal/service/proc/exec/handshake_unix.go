//go:build unix

// Package exec — the parent<->trampoline handshake pipe. The re-exec trampoline
// (trampoline_unix.go) applies rlimits/umask in a forked child before exec'ing
// the real target; if that application fails, or the execve itself fails, the
// child exits with a distinct status — but Start could not otherwise tell that
// apart from a legitimate target exit of the same code. This pipe closes the gap:
// the child inherits the write end as handshakeFD and reports a one-byte status
// through it, so Start surfaces a typed RlimitFailed / SpawnFailed instead of a
// silent spawn. It is the same self-pipe + close-on-exec trick os/exec uses for
// its own errpipe: a clean execve closes the fd (the parent reads EOF = success),
// a failure writes a status byte first.
package exec

import (
	"io"
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// handshakeFD is the child file descriptor carrying the trampoline status. It is
// the descriptor after the three std streams (0,1,2), since Start appends the
// pipe write end right after stdio in the child's file table.
const handshakeFD int = 3

// Status bytes the trampoline writes to handshakeFD before it exits on failure.
const (
	handshakeApplyFail  byte = 'A' // a setrlimit/umask application failed.
	handshakeExecFail   byte = 'E' // the execve of the real target failed.
	handshakeCgroupFail byte = 'C' // the pre-exec cgroup.procs placement failed.
)

// handshake is the parent side of the trampoline status pipe: the child inherits
// the write end as handshakeFD and reports a failure byte through it, while the
// parent reads the read end to learn the trampoline's outcome.
type handshake struct {
	// read is the parent's read end; EOF means the trampoline exec'd the target.
	read *os.File
	// write is the child's inherited write end, handed to os.StartProcess.
	write *os.File
}

// newHandshake creates the status pipe. The returned write end must be placed at
// handshakeFD in the child's file table (right after the three std streams).
func newHandshake() (hs *handshake, err error) {
	r, w, pErr := os.Pipe()
	//: a pipe-setup failure means the spawn cannot be observed — surface it.
	if pErr != nil {
		//: propagate the OS cause for the caller to wrap as RlimitFailed.
		return nil, pErr
	}
	//: the parent keeps the read end; the child inherits the write end.
	return &handshake{read: r, write: w}, nil
}

// childFile returns the write end to hand to os.StartProcess as handshakeFD.
func (h *handshake) childFile() *os.File {
	//: the child inherits this as fd handshakeFD and reports failures through it.
	return h.write
}

// await closes the parent's write end and reads the trampoline's outcome: a nil
// return on an empty read (EOF) means the execve succeeded and the target is
// running; any bytes return the matching typed sentinel, and the caller must reap
// the exited child.
func (h *handshake) await() error {
	//: drop the parent's write end so the read reaches EOF on a clean execve.
	swallowErr(h.write.Close())
	status, rErr := io.ReadAll(h.read)
	//: a read fault leaves status empty; treated as success below (conservative).
	swallowErr(rErr)
	//: release the read end now the trampoline's outcome is known.
	swallowErr(h.read.Close())
	//: an empty status (EOF) means the execve succeeded and the target is live.
	if len(status) == 0 {
		//: the trampoline applied the limits and exec'd the target cleanly.
		return nil
	}
	//: map the trampoline's status byte to the central typed sentinel.
	return handshakeError(status[0])
}

// closeBoth releases both pipe ends when the spawn aborts before await runs.
func (h *handshake) closeBoth() {
	//: release the parent's read and write ends on an aborted spawn.
	swallowErr(h.read.Close())
	swallowErr(h.write.Close())
}

// closeHandshake releases a (possibly nil) handshake pipe on an aborted spawn.
func closeHandshake(hs *handshake) {
	//: a spawn that never created a handshake has nothing to release.
	if hs == nil {
		//: nothing to do for a non-trampolined spawn.
		return
	}
	//: release the pipe ends of an aborted trampolined spawn.
	hs.closeBoth()
}

// handshakeError maps a trampoline status byte to its central typed sentinel.
func handshakeError(code byte) error {
	//: the status byte distinguishes a limit-apply failure from an exec failure.
	switch code {
	//: the trampoline could not apply a requested rlimit/umask.
	case handshakeApplyFail:
		//: an unhonourable limit is the central RlimitFailed sentinel.
		return coreproc.RlimitFailed
	//: the trampoline could not place the child into the requested cgroup.
	case handshakeCgroupFail:
		//: a refused cgroup.procs write is the central CgroupWriteFailed sentinel.
		return coreproc.CgroupWriteFailed
	//: the trampoline could not execve the real target.
	case handshakeExecFail:
		//: a failed execve is the central SpawnFailed sentinel.
		return coreproc.SpawnFailed
	//: any unrecognised byte is still a spawn that did not run the target.
	default:
		//: treat an unknown status as a generic spawn failure.
		return coreproc.SpawnFailed
	}
}
