//go:build linux || freebsd

package notify

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// payloadBufSize bounds a received datagram body; sd_notify payloads are short
// text blocks, so 4 KiB comfortably holds any realistic field set.
const payloadBufSize int = 4096

// maxPassedFiles is SCM_MAX_FD on Linux, the most descriptors one datagram
// carries; FreeBSD has no such bound of its own, and its control buffer is
// sized to the same 253.
const maxPassedFiles int = 253

// fdBytes is the size of one descriptor in a control message.
const fdBytes int = 4

// listener is the Listener implementation on Linux and FreeBSD: it owns a
// unixgram socket on which the kernel stamps every datagram with its sender's
// credentials — SO_PASSCRED on Linux, LOCAL_CREDS_PERSISTENT on FreeBSD
// (enableCreds) —, and removes its backing path (if any) on Close.
type listener struct {
	conn    *net.UnixConn
	path    string // filesystem path to unlink on Close; empty for abstract sockets
	tempDir string // private temp dir to remove on Close; empty when none
}

// Listen creates a supervisor-side sd_notify datagram socket, makes the kernel
// stamp every datagram with its sender's credentials — SO_PASSCRED on Linux,
// LOCAL_CREDS_PERSISTENT on FreeBSD —, and returns the Listener together with
// the $NOTIFY_SOCKET value a child should be given. The socket is bound under a
// private temp directory so its path is unguessable; the caller sets
// NOTIFY_SOCKET=socketPath for the child.
//
// Its Recv reads the whole sd_notify(3) transport (kitsunium/sdk#730): the
// sender's PID, real user and real group from SCM_CREDENTIALS or SCM_CREDS2, a
// pidfd on the sender where the kernel attaches one (SO_PASSPIDFD, Linux 6.5),
// and the descriptors a datagram carries (SCM_RIGHTS, close-on-exec on
// arrival). Every assignment sd_notify(3)
// defines is checked — a flag is 1, a PID or a count is decimal, NOTIFYACCESS= a
// known access, FDNAME= a valid name —; an unknown name is kept as it came, a
// name given twice keeps its last value. BARRIER=1 must carry exactly one
// descriptor and is returned alone; MAINPIDFD=1 exactly one pidfd — a process
// descriptor on FreeBSD —, whose PID becomes MainPID. A datagram refused — truncated, its control data truncated,
// an embedded NUL, an assignment out of bounds, no credentials — is
// InvalidNotification or CredentialMismatch, and every descriptor it carried is
// closed.
func Listen() (l coreproc.Listener, socketPath string, err error) {
	//: a private 0700 temp dir keeps the socket path unguessable and isolated.
	dir, err := os.MkdirTemp("", tempDirPrefix)
	//: without a directory there is nowhere to bind the socket.
	if err != nil {
		//: wrap the mkdir fault as ListenFailed.
		return nil, "", wrapListen(err, "")
	}
	//: on any error after this point, remove the temp dir so nothing leaks.
	defer func() {
		//: the success path transfers ownership of the dir to the listener.
		if err != nil {
			//: join the cleanup error so a failed removal is not silently lost.
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	//: the socket lives at a fixed name inside the private directory.
	path := filepath.Join(dir, socketName)
	//: bind a datagram socket at the chosen path.
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	//: a bind failure leaves nothing usable; the deferred cleanup removes the dir.
	if err != nil {
		//: wrap the bind fault as ListenFailed.
		return nil, "", wrapListen(err, path)
	}
	//: on any error after the socket exists, close it too (in addition to the dir).
	defer func() {
		//: only tear the socket down when the constructor is failing.
		if err != nil {
			//: join the close error so a failed close is surfaced, not dropped.
			err = errors.Join(err, conn.Close())
		}
	}()
	//: the kernel attaches the sender's credentials to every datagram.
	if err = enableCreds(conn); err != nil {
		//: the deferred closures release the socket and the temp dir.
		return nil, "", err
	}
	//: hand back the live listener plus the path a child should be pointed at.
	return &listener{conn: conn, path: path, tempDir: dir}, path, nil
}

// ListenFor is Listen for a service that runs as uid and gid: the private
// directory and the socket in it belong to that user, so the service — and
// root — can reach the socket, and no other user can. Changing their owner
// needs the privilege to: a refusal is ListenFailed, nothing left behind.
func ListenFor(uid, gid int) (l coreproc.Listener, socketPath string, err error) {
	l, socketPath, err = Listen()
	//: no socket.
	if err != nil {
		//: Listen's refusal.
		return nil, "", err
	}
	ln, _ := l.(*listener)
	//: the directory first, then the socket.
	for _, p := range []string{ln.tempDir, ln.path} {
		//: a refusal: no privilege to give files away.
		if cErr := os.Lchown(p, uid, gid); cErr != nil {
			//: nothing left behind: the socket closed, the directory removed.
			return nil, "", errors.Join(wrapListen(cErr, ln.path), ln.Close())
		}
	}
	//: the service's socket.
	return l, socketPath, nil
}

// Recv blocks for the next datagram and returns it parsed (see Listen): the
// kernel-verified sender, the descriptors it carried, every assignment
// sd_notify(3) defines within its bounds. A datagram it refuses has every
// descriptor it carried closed. The descriptors arrive close-on-exec,
// atomically (MSG_CMSG_CLOEXEC, read): no spawn running meanwhile inherits one.
func (l *listener) Recv() (value coreproc.NotificationValue, err error) {
	//: a control buffer for every control message one datagram has.
	return l.recv(oobBufSize())
}

// recv is Recv with a control buffer of oobSize bytes: a smaller one makes
// the kernel truncate the control data (MSG_CTRUNC), which the tests need.
func (l *listener) recv(oobSize int) (value coreproc.NotificationValue, err error) {
	buf := make([]byte, payloadBufSize)
	oob := make([]byte, oobSize)
	n, oobn, recvFlags, err := l.read(buf, oob)
	//: a read fault (including a Close-induced unblock) carries no descriptor.
	if err != nil {
		//: wrap the read fault as ListenFailed.
		return coreproc.NotificationValue{}, wrapListen(err, l.path)
	}
	d := parseControl(oob[:oobn])
	value, err = d.value(buf[:n], recvFlags, n == len(buf))
	//: refused: nothing it carried survives.
	if err != nil {
		d.closeAll()
		//: INVALID_NOTIFICATION or CREDENTIAL_MISMATCH.
		return coreproc.NotificationValue{}, err
	}
	//: the datagram, its descriptors now the caller's.
	return value, nil
}

// sender is a datagram's sender as the kernel stamped it: SCM_CREDENTIALS on
// Linux, SCM_CREDS2 on FreeBSD.
type sender struct {
	pid, uid, gid int
}

// control is what a datagram's control messages carried.
type control struct {
	// cred is the sender's credentials, nil when absent.
	cred *sender
	// files are SCM_RIGHTS' descriptors, in order.
	files []int
	// pidfd is SCM_PIDFD's descriptor, -1 when absent.
	pidfd int
}

// parseControl reads the control messages: the credentials, the descriptors,
// the sender's pidfd. Every descriptor is recorded, so a refusal closes them.
func parseControl(oob []byte) *control {
	d := &control{pidfd: -1}
	scms, err := syscall.ParseSocketControlMessage(oob)
	//: the kernel writes well-formed control data; a malformed tail holds none.
	if err != nil {
		//: what could be read.
		return d
	}
	//: each message, by type.
	for i := range scms {
		d.take(&scms[i])
	}
	//: the credentials and the descriptors.
	return d
}

// closeAll closes every descriptor the datagram carried.
func (d *control) closeAll() {
	//: the passed descriptors.
	for _, fd := range d.files {
		swallowErr(syscall.Close(fd))
	}
	d.files = nil
	//: the sender's pidfd.
	if d.pidfd >= 0 {
		swallowErr(syscall.Close(d.pidfd))
		d.pidfd = -1
	}
}

// value checks the datagram and builds its NotificationValue, handing over
// the descriptors: the barrier, the main process's pidfd, the ones to store,
// the sender's pidfd. Descriptors no assignment calls for are closed.
func (d *control) value(body []byte, recvFlags int, full bool) (coreproc.NotificationValue, error) {
	//: the datagram itself must be whole and plain.
	if err := checkDatagram(body, recvFlags, full); err != nil {
		//: INVALID_NOTIFICATION.
		return coreproc.NotificationValue{}, err
	}
	//: the socket option guarantees credentials; their absence is a credential
	//: failure.
	if d.cred == nil {
		//: CREDENTIAL_MISMATCH.
		return coreproc.NotificationValue{}, wrapCredMismatch()
	}
	value, err := parsePayload(string(body))
	//: a malformed line or an assignment out of bounds.
	if err != nil {
		//: INVALID_NOTIFICATION.
		return coreproc.NotificationValue{}, err
	}
	//: the descriptors, by the assignments that call for them.
	if err = d.handOverFiles(&value); err != nil {
		//: a barrier or a pidfd without its descriptor.
		return coreproc.NotificationValue{}, err
	}
	value.SenderPID = d.cred.pid
	value.SenderUID = d.cred.uid
	value.SenderGID = d.cred.gid
	//: the sender's pidfd, the caller's now.
	if d.pidfd >= 0 {
		descriptorsOf(&value).SenderPIDFD = os.NewFile(uintptr(d.pidfd), "pidfd")
		d.pidfd = -1
	}
	//: the datagram.
	return value, nil
}

// checkDatagram refuses a truncated datagram, truncated control data, and a
// body with an embedded NUL.
func checkDatagram(body []byte, recvFlags int, full bool) error {
	//: MSG_TRUNC, or a payload that exactly filled the buffer: a partial body.
	if recvFlags&syscall.MSG_TRUNC != 0 || full {
		//: never acted on clipped.
		return wrapInvalid(nil, errs.String("reason", "datagram truncated"))
	}
	//: MSG_CTRUNC: control data lost — descriptors or credentials.
	if recvFlags&syscall.MSG_CTRUNC != 0 {
		//: refused, as systemd refuses it.
		return wrapInvalid(nil, errs.String("reason", "control data truncated"))
	}
	//: systemd ignores a message with an embedded NUL.
	if bytes.IndexByte(body, 0) >= 0 {
		//: refused.
		return wrapInvalid(nil, errs.String("reason", "embedded NUL"))
	}
	//: whole and plain.
	return nil
}

// handOverFiles gives value the descriptors its assignments call for, as
// systemd's manager does: BARRIER=1 is one descriptor and nothing else is
// read from its datagram; MAINPIDFD=1 takes the first descriptor as the main
// process's pidfd; FDSTORE=1 keeps the rest; any other is closed.
func (d *control) handOverFiles(value *coreproc.NotificationValue) error {
	//: a barrier: one descriptor, closed by the caller once everything before
	//: it is processed; the datagram's other assignments are dropped.
	if value.State["BARRIER"] == "1" {
		//: exactly one.
		if len(d.files) != 1 {
			//: refused.
			return wrapInvalid(nil, errs.String("reason", "BARRIER=1 without exactly one descriptor"))
		}
		*value = coreproc.NotificationValue{
			State:       map[string]string{"BARRIER": "1"},
			Descriptors: &coreproc.DescriptorsValue{Files: d.take1()},
		}
		//: the barrier alone.
		return nil
	}
	//: the main process's pidfd.
	if value.State["MAINPIDFD"] == "1" {
		//: the main process, by its pidfd.
		if err := d.mainPIDFD(value); err != nil {
			//: no pidfd.
			return err
		}
	}
	//: FDSTORE=1: the rest are the caller's to store.
	if value.State["FDSTORE"] == "1" && len(d.files) > 0 {
		descriptorsOf(value).Files = d.takeAll()
	}
	//: descriptors nothing called for.
	d.closeAll()
	//: handed over.
	return nil
}

// mainPIDFD takes the first descriptor as the new main process's pidfd and
// its PID as MainPID: a pidfd's on Linux, read from /proc/self/fdinfo, a
// process descriptor's on FreeBSD, read by pdgetpid(2) (pidfdPID).
func (d *control) mainPIDFD(value *coreproc.NotificationValue) error {
	//: MAINPIDFD=1 calls for one.
	if len(d.files) == 0 {
		//: refused.
		return wrapInvalid(nil, errs.String("reason", "MAINPIDFD=1 without a descriptor"))
	}
	pid, err := pidfdPID(d.files[0])
	//: not a pidfd, or its process gone.
	if err != nil {
		//: refused, the cause kept.
		return wrapInvalid(err, errs.String("reason", "MAINPIDFD=1 descriptor is no live pidfd"))
	}
	f := d.take1()
	descriptorsOf(value).MainPIDFD = f[0]
	value.MainPID = pid
	//: the main process.
	return nil
}

// descriptorsOf returns value's Descriptors, made on first use.
func descriptorsOf(value *coreproc.NotificationValue) *coreproc.DescriptorsValue {
	//: none yet.
	if value.Descriptors == nil {
		value.Descriptors = &coreproc.DescriptorsValue{}
	}
	//: the datagram's.
	return value.Descriptors
}

// take1 hands over the first descriptor.
func (d *control) take1() []*os.File {
	f := os.NewFile(uintptr(d.files[0]), "notify-fd")
	d.files = d.files[1:]
	//: the caller's.
	return []*os.File{f}
}

// takeAll hands over every remaining descriptor.
func (d *control) takeAll() []*os.File {
	out := make([]*os.File, 0, len(d.files))
	//: in the order they came.
	for _, fd := range d.files {
		out = append(out, os.NewFile(uintptr(fd), "notify-fd"))
	}
	d.files = nil
	//: the caller's.
	return out
}

// swallowErr discards a cleanup error nobody can act on.
func swallowErr(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		return
	}
	//: a close of a descriptor being dropped: nothing to do.
}

// wrapCredMismatch restates the central CredentialMismatch sentinel fields for a
// datagram that arrived without parseable SO_PASSCRED credentials.
func wrapCredMismatch() error {
	//: copy the exact sentinel strings so the error matches CodeCredentialMismatch.
	return errs.Wrap(nil, errs.WrapParams{
		Code:     coreproc.CodeCredentialMismatch,
		Reason:   "CREDENTIAL_MISMATCH",
		Public:   "Datagram sender credentials did not match",
		Private:  "service/proc/systemd/notify.Recv: SO_PASSCRED sender pid is not the expected supervised process",
		ExitCode: exitNoPerm,
	})
}

// Close releases the socket, unblocks any in-flight Recv, and removes the backing
// temp directory (pathname sockets only; abstract sockets vanish on close).
func (l *listener) Close() error {
	//: closing the conn unblocks a pending Recv and frees the fd.
	err := l.conn.Close()
	//: remove the private temp dir (and the socket inode within it) when present.
	if l.tempDir != "" {
		//: join any removal fault so cleanup errors are not silently dropped.
		err = errors.Join(err, os.RemoveAll(l.tempDir))
	}
	//: a pathname socket outside a temp dir still needs its inode unlinked.
	if l.tempDir == "" && l.path != "" && !strings.HasPrefix(l.path, "\x00") {
		//: join any unlink fault for the same reason.
		err = errors.Join(err, os.Remove(l.path))
	}
	//: nothing failed — report success.
	if err == nil {
		//: the listener is fully torn down.
		return nil
	}
	//: wrap the combined teardown fault as ListenFailed.
	return wrapListen(err, l.path)
}

// wrapListen restates the ListenFailed sentinel fields around a stdlib/syscall
// cause, attaching the socket path for diagnostics.
func wrapListen(cause error, path string) error {
	//: copy the exact sentinel strings so the wrapped error matches CodeListenFailed.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeListenFailed,
		Reason:   "LISTEN_FAILED",
		Public:   "Could not create the sd_notify listener socket",
		Private:  "service/proc/systemd/notify.Listen: creating or binding the AF_UNIX datagram socket failed",
		ExitCode: exitOSErr,
	}, errs.String("socket", path))
}
