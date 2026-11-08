//go:build freebsd

package notify

import (
	"net"
	"syscall"
	"unsafe"

	"github.com/kitsunium/sdk/internal/service/proc/internal/bsdabi"
)

// FreeBSD's credential passing (sys/un.h, sys/socket.h); the stdlib exports
// none of these names.
const (
	// solLocal is SOL_LOCAL, the level of a unix(4) socket's own options.
	solLocal int = 0
	// localCredsPersistent is LOCAL_CREDS_PERSISTENT (FreeBSD 13): every
	// datagram received then carries its sender's credentials.
	localCredsPersistent int = 3
	// scmCreds2 is SCM_CREDS2, the control message LOCAL_CREDS_PERSISTENT
	// adds: a struct sockcred2.
	scmCreds2 int32 = 0x08
	// sockcred2Max is SOCKCRED2SIZE(CMGROUP_MAX): 28 bytes and 16 groups.
	sockcred2Max int = 28 + 4*16
	// cmsgcredSize is sizeof(struct cmsgcred): the SCM_CREDS a sender may ask
	// the kernel to add, which is not read but must fit.
	cmsgcredSize int = 84
)

// oobBufSize is the control buffer one datagram needs at most: SCM_CREDS2
// with CMGROUP_MAX groups, SCM_RIGHTS with as many descriptors as Linux's
// SCM_MAX_FD, and a sender's own SCM_CREDS.
func oobBufSize() int {
	//: the three messages, each aligned.
	return syscall.CmsgSpace(sockcred2Max) + syscall.CmsgSpace(fdBytes*maxPassedFiles) + syscall.CmsgSpace(cmsgcredSize)
}

// enableCreds sets LOCAL_CREDS_PERSISTENT on the listener's socket, so each
// received datagram carries an SCM_CREDS2 control message the kernel fills:
// the sender's PID, real and effective users and groups. A sender cannot
// forge it: the kernel refuses SCM_CREDS2 in a message sent (unp_internalize).
func enableCreds(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	//: without the raw conn the option cannot be set.
	if err != nil {
		//: ListenFailed.
		return wrapListen(err, "")
	}
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		//: credentials with every datagram, not only the first (LOCAL_CREDS).
		sockErr = syscall.SetsockoptInt(int(fd), solLocal, localCredsPersistent, 1)
	})
	//: the closure may not have run, or the kernel refused the option.
	if ctrlErr != nil || sockErr != nil {
		//: no credentials would come: ListenFailed.
		return wrapListen(errorOr(ctrlErr, sockErr), "")
	}
	//: every later datagram carries its sender.
	return nil
}

// read receives one datagram with recvmsg(2) and MSG_CMSG_CLOEXEC: the
// descriptors it carries arrive close-on-exec, atomically, so no spawn in
// flight inherits one — Go's ReadMsgUnix sets the flag only afterwards on
// FreeBSD. The socket is non-blocking: the runtime's poller waits for it.
func (l *listener) read(buf, oob []byte) (n, oobn, recvFlags int, err error) {
	raw, err := l.conn.SyscallConn()
	//: a closed listener.
	if err != nil {
		//: the cause.
		return 0, 0, 0, err
	}
	var rErr error
	err = raw.Read(func(fd uintptr) bool {
		n, oobn, recvFlags, _, rErr = syscall.Recvmsg(int(fd), buf, oob, syscall.MSG_CMSG_CLOEXEC)
		//: nothing yet: the poller waits for the next datagram.
		return rErr != syscall.EAGAIN && rErr != syscall.EINTR
	})
	//: the datagram, or the read's or the socket's fault.
	return n, oobn, recvFlags, errorOr(err, rErr)
}

// take records one control message: the descriptors, and the sender the
// kernel stamped (SCM_CREDS2, bsdabi.Sockcred2). A sender's own SCM_CREDS is
// left aside.
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
	//: the sender's pid, real uid and real gid.
	case scmCreds2:
		pid, uid, gid, ok := bsdabi.Sockcred2(scm.Data)
		//: a struct sockcred2 this build reads.
		if ok {
			d.cred = &sender{pid: pid, uid: uid, gid: gid}
		}
	}
}

// pidfdPID reads the PID of a process descriptor: FreeBSD has no pidfd, and
// its pdfork(2) descriptors are what MAINPIDFD=1 can carry here. pdgetpid(2)
// refuses any other descriptor (EBADF).
func pidfdPID(fd int) (int, error) {
	var pid int32
	//: pdgetpid(fd, &pid).
	_, _, errno := syscall.Syscall(syscall.SYS_PDGETPID, uintptr(fd), uintptr(unsafe.Pointer(&pid)), 0)
	//: not a process descriptor.
	if errno != 0 {
		//: the kernel's word.
		return 0, errno
	}
	//: a process this descriptor still names.
	if pid > 0 {
		//: its PID.
		return int(pid), nil
	}
	//: no process.
	return 0, syscall.ESRCH
}

// errorOr returns err, or alt when err is nil.
func errorOr(err, alt error) error {
	//: the first cause known.
	if err != nil {
		//: err itself.
		return err
	}
	//: the substitute.
	return alt
}
