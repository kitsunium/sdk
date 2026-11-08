//go:build linux

package notify

import (
	"encoding/binary"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// soPassPIDFD is SO_PASSPIDFD (Linux 6.5, asm-generic 76 on every
// architecture this package builds for; the stdlib exports no name): every
// datagram then carries a pidfd on its sender (SCM_PIDFD).
const soPassPIDFD int = 76

// scmPIDFD is SCM_PIDFD, the control message type of that pidfd.
const scmPIDFD int32 = 0x04

// oobBufSize is the control buffer one datagram needs at most: SCM_RIGHTS
// with SCM_MAX_FD descriptors, SCM_CREDENTIALS, and SCM_PIDFD.
func oobBufSize() int {
	//: the three messages, each aligned.
	return syscall.CmsgSpace(fdBytes*maxPassedFiles) + syscall.CmsgSpace(syscall.SizeofUcred) + syscall.CmsgSpace(fdBytes)
}

// enableCreds flips SO_PASSCRED on the listener's underlying fd so each
// received datagram carries an SCM_CREDENTIALS control message.
func enableCreds(conn *net.UnixConn) error {
	//: a raw conn exposes the fd needed for setsockopt without leaking it.
	raw, err := conn.SyscallConn()
	//: without the raw conn the option cannot be set.
	if err != nil {
		//: wrap the SyscallConn fault as ListenFailed.
		return wrapListen(err, "")
	}
	//: capture the setsockopt result from inside the fd callback.
	var sockErr error
	//: Control runs the closure with the fd pinned valid for its duration.
	ctrlErr := raw.Control(func(fd uintptr) {
		//: SO_PASSCRED at SOL_SOCKET requests per-message sender credentials.
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1)
		//: SO_PASSPIDFD adds a pidfd on the sender; a kernel before 6.5 refuses
		//: the option, and the PID SO_PASSCRED names is all there is.
		swallowErr(syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soPassPIDFD, 1))
	})
	//: a Control failure means the closure may not have run.
	if ctrlErr != nil {
		//: wrap the Control fault as ListenFailed.
		return wrapListen(ctrlErr, "")
	}
	//: a setsockopt failure means credentials would be unavailable.
	if sockErr != nil {
		//: wrap the setsockopt fault as ListenFailed.
		return wrapListen(sockErr, "")
	}
	//: SO_PASSCRED is now active for every subsequent datagram.
	return nil
}

// read receives one datagram. Go's ReadMsgUnix passes MSG_CMSG_CLOEXEC on
// Linux, so every descriptor it carried arrives close-on-exec, atomically.
func (l *listener) read(buf, oob []byte) (n, oobn, recvFlags int, err error) {
	n, oobn, recvFlags, _, err = l.conn.ReadMsgUnix(buf, oob)
	//: the datagram, its control data and the kernel's flags.
	return n, oobn, recvFlags, err
}

// take records one control message.
func (d *control) take(scm *syscall.SocketControlMessage) {
	//: only the socket level carries these.
	if scm.Header.Level != syscall.SOL_SOCKET {
		return
	}
	//: by type.
	switch scm.Header.Type {
	//: the descriptors the sender passed.
	case syscall.SCM_RIGHTS:
		fds, err := syscall.ParseUnixRights(scm)
		//: whole messages only.
		if err == nil {
			d.files = append(d.files, fds...)
		}
	//: the sender's pid, uid and gid.
	case syscall.SCM_CREDENTIALS:
		cred, err := syscall.ParseUnixCredentials(scm)
		//: a ucred.
		if err == nil {
			d.cred = &sender{pid: int(cred.Pid), uid: int(cred.Uid), gid: int(cred.Gid)}
		}
	//: a pidfd on the sender.
	case scmPIDFD:
		//: one int.
		if len(scm.Data) >= fdBytes {
			d.pidfd = int(int32(binary.NativeEndian.Uint32(scm.Data)))
		}
	}
}

// pidfdPID reads the PID a pidfd refers to from /proc/self/fdinfo: its Pid:
// line, absent for any other descriptor, -1 once the process is reaped.
func pidfdPID(fd int) (int, error) {
	raw, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(fd))
	//: no fdinfo.
	if err != nil {
		//: the cause.
		return 0, err
	}
	//: line by line.
	for line := range strings.SplitSeq(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "Pid:")
		//: not the line.
		if !ok {
			continue
		}
		pid, pErr := strconv.Atoi(strings.TrimSpace(rest))
		//: a live process.
		if pErr == nil && pid > 0 {
			//: its PID.
			return pid, nil
		}
		//: a reaped process (-1), or garbage.
		return 0, syscall.ESRCH
	}
	//: no Pid: line: not a pidfd.
	return 0, syscall.EBADF
}
