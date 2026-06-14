//go:build linux

// Package sdnotify — the supervisor (listener) side on Linux: SO_PASSCRED.
package sdnotify

import (
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

// oobBufSize bounds the out-of-band control buffer that carries the single
// SCM_CREDENTIALS message; 1 KiB is far larger than one ucred cmsg.
const oobBufSize int = 1024

// listener is the Linux Listener implementation: it owns a unixgram socket with
// SO_PASSCRED enabled so every datagram arrives with the kernel-stamped sender
// credentials, and removes its backing path (if any) on Close.
type listener struct {
	conn    *net.UnixConn
	path    string // filesystem path to unlink on Close; empty for abstract sockets
	tempDir string // private temp dir to remove on Close; empty when none
}

// Listen creates a supervisor-side sd_notify datagram socket, enables SO_PASSCRED
// on it, and returns the Listener together with the $NOTIFY_SOCKET value a child
// should be given. The socket is bound under a private temp directory so its path
// is unguessable; the caller sets NOTIFY_SOCKET=socketPath for the child.
func Listen() (l coreproc.Listener, socketPath string, err error) {
	//: a private 0700 temp dir keeps the socket path unguessable and isolated.
	dir, err := os.MkdirTemp("", "sdnotify-")
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
	path := filepath.Join(dir, "notify")
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
	//: enable SO_PASSCRED so the kernel attaches sender credentials per datagram.
	if err = enablePasscred(conn); err != nil {
		//: the deferred closures release the socket and the temp dir.
		return nil, "", err
	}
	//: hand back the live listener plus the path a child should be pointed at.
	return &listener{conn: conn, path: path, tempDir: dir}, path, nil
}

// enablePasscred flips SO_PASSCRED on the listener's underlying fd so each
// received datagram carries an SCM_CREDENTIALS control message.
func enablePasscred(conn *net.UnixConn) error {
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

// Recv blocks for the next datagram, parses its payload into a NotificationValue,
// and fills SenderPID from the kernel-verified SO_PASSCRED credentials.
func (l *listener) Recv() (value coreproc.NotificationValue, err error) {
	//: size buffers for the small text payload plus its OOB credential cmsg.
	buf := make([]byte, payloadBufSize)
	oob := make([]byte, oobBufSize)
	//: ReadMsgUnix returns the payload, the credential control message, and the
	//: kernel recv flags (MSG_TRUNC is set when the datagram overran buf).
	n, oobn, recvFlags, _, err := l.conn.ReadMsgUnix(buf, oob)
	//: a read fault (including a Close-induced unblock) is reported as ListenFailed.
	if err != nil {
		//: wrap the read fault as ListenFailed.
		return coreproc.NotificationValue{}, wrapListen(err, l.path)
	}
	//: a truncated datagram (MSG_TRUNC, or a payload that exactly filled buf)
	//: would parse into a partial NAME=value body — reject it as malformed
	//: rather than acting on a silently-clipped notification.
	if recvFlags&syscall.MSG_TRUNC != 0 || n == len(buf) {
		//: surface InvalidNotification for the truncated datagram.
		return coreproc.NotificationValue{}, wrapInvalid(nil, errs.String("reason", "datagram truncated"))
	}
	//: parse the newline-separated NAME=value body.
	value, err = parsePayload(string(buf[:n]))
	//: a malformed body is InvalidNotification (already typed by parsePayload).
	if err != nil {
		//: propagate the parse error unchanged.
		return coreproc.NotificationValue{}, err
	}
	//: extract the kernel-verified sender PID from the control message.
	pid, ok := senderPID(oob[:oobn])
	//: missing/unparseable credentials must NOT be accepted as a valid PID 0;
	//: SO_PASSCRED guarantees a ucred, so its absence is a credential failure.
	if !ok {
		//: surface the central CredentialMismatch sentinel for the unattributable datagram.
		return coreproc.NotificationValue{}, wrapCredMismatch()
	}
	//: record the kernel-verified sender PID on the notification.
	value.SenderPID = pid
	//: hand back the fully populated notification.
	return value, nil
}

// wrapCredMismatch restates the central CredentialMismatch sentinel fields for a
// datagram that arrived without parseable SO_PASSCRED credentials.
func wrapCredMismatch() error {
	//: copy the exact sentinel strings so the error matches CodeCredentialMismatch.
	return errs.Wrap(nil, errs.WrapParams{
		Code:     coreproc.CodeCredentialMismatch,
		Reason:   "CREDENTIAL_MISMATCH",
		Public:   "Datagram sender credentials did not match",
		Private:  "service/proc/sdnotify.Recv: SO_PASSCRED sender pid is not the expected supervised process",
		ExitCode: exitNoPerm,
	})
}

// senderPID extracts the SO_PASSCRED sender PID from a datagram's OOB control
// data. ok is false when no parseable SCM_CREDENTIALS message is present, so the
// caller can reject an unattributable datagram rather than trust a zero PID.
func senderPID(oob []byte) (pid int, ok bool) {
	//: an empty control buffer carries no credentials.
	if len(oob) == 0 {
		//: signal "no credentials" so the caller does not accept PID 0.
		return 0, false
	}
	//: split the OOB blob into individual socket control messages.
	scms, err := syscall.ParseSocketControlMessage(oob)
	//: malformed control data yields no credentials rather than an error here.
	if err != nil {
		//: signal "no credentials" so the caller does not accept PID 0.
		return 0, false
	}
	//: scan for the first SCM_CREDENTIALS message and read its ucred.
	for i := range scms {
		//: parse this control message as a ucred (pid/uid/gid).
		cred, perr := syscall.ParseUnixCredentials(&scms[i])
		//: a non-credential message simply does not parse; try the next one.
		if perr != nil {
			//: skip messages that are not SCM_CREDENTIALS.
			continue
		}
		//: the kernel-stamped PID is exactly what we want to surface.
		return int(cred.Pid), true
	}
	//: no credential message was found.
	return 0, false
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
		Private:  "service/proc/sdnotify.Listen: creating or binding the AF_UNIX datagram socket failed",
		ExitCode: exitOSErr,
	}, errs.String("socket", path))
}
