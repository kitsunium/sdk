package service

import (
	"context"
	"os"
	"slices"
	"strconv"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/exec"
	svcnotify "github.com/kitsunium/sdk/internal/service/proc/systemd/notify"
)

// execSpawner is the default Spawner: proc/exec's Start, which returns once
// the program was executed.
type execSpawner struct{}

// Spawn starts spec with proc/exec.
func (execSpawner) Spawn(ctx context.Context, spec coreproc.Spec) (coreproc.Process, error) {
	//: the SDK's spawn engine.
	return exec.Start(ctx, spec)
}

// NewAttacher is the Attacher over proc/exec (kitsunium/sdk#729): AttachPID
// adopts with exec.Attach, AttachPIDFile reads the file with exec.AttachPIDFile
// bounded by scope — the cgroup and the users a process named by a PID file
// nobody but root or this process's user vouches for must lie in; the unit's
// own cgroup and user, typically, the cgroup as /proc/<pid>/cgroup names it:
// proc/cgroup's Membership turns a group's directory into that path. The
// processes it returns are held through a
// pidfd on Linux, through kqueue knotes on darwin and the BSDs, and are not
// the engine's children: their Wait returns STATUS_UNKNOWN once they are gone,
// which the engine reads as it reads EXIT_UNKNOWN. Off Linux a scope naming a
// cgroup is ADOPTION_REFUSED (there is none to lie in); each method is
// UNSUPPORTED_PLATFORM on illumos, Solaris and Windows.
func NewAttacher(scope coreproc.AttachValue) coresvc.Attacher {
	//: the scope is copied: a caller's later change to UIDs reaches nothing.
	scope.UIDs = slices.Clone(scope.UIDs)
	//: the adapter.
	return execAttacher{scope: scope}
}

// execAttacher is NewAttacher's Attacher.
type execAttacher struct {
	scope coreproc.AttachValue
}

// AttachPID adopts pid with exec.Attach.
func (execAttacher) AttachPID(ctx context.Context, pid int) (coreproc.Process, error) {
	//: the SDK's adoption.
	return exec.Attach(ctx, pid)
}

// AttachPIDFile reads path with exec.AttachPIDFile, bounded by the scope.
func (a execAttacher) AttachPIDFile(ctx context.Context, path string) (coreproc.Process, error) {
	//: the SDK's safe read.
	return exec.AttachPIDFile(ctx, path, a.scope)
}

// ListenNotify opens an sd_notify socket for one unit, over proc/systemd's
// listener: the sender's PID and user are the kernel's (SO_PASSCRED on Linux,
// LOCAL_CREDS_PERSISTENT on FreeBSD). It is UNSUPPORTED_PLATFORM elsewhere,
// where no socket names its sender. The unit that takes it in Config.Notify
// closes it.
func ListenNotify() (coresvc.NotifySource, error) {
	l, path, err := svcnotify.Listen()
	//: no socket.
	if err != nil {
		//: the listener's refusal.
		return nil, err
	}
	//: the socket.
	return &listenerSource{l: l, socket: path}, nil
}

// listenerSource adapts proc/systemd's Listener to a NotifySource.
type listenerSource struct {
	l      coreproc.Listener
	socket string
}

// Socket is the socket's path.
func (s *listenerSource) Socket() string {
	//: the path.
	return s.socket
}

// Recv returns the next datagram. ctx is read before the wait: the wait ends
// with Close. A datagram the listener refused — malformed, out of bounds,
// unattributed — is returned unverified, which the engine counts as refused:
// a service sending garbage cannot end the reader by failing its Recv.
func (s *listenerSource) Recv(ctx context.Context) (coresvc.NotifyValue, error) {
	//: the caller gave up already.
	if err := ctx.Err(); err != nil {
		//: its error.
		return coresvc.NotifyValue{}, err
	}
	v, err := s.l.Recv()
	//: one datagram refused: counted, the socket kept.
	if errs.HasCode(err, coreproc.CodeInvalidNotification) || errs.HasCode(err, coreproc.CodeCredentialMismatch) {
		//: unverified, so refused.
		return coresvc.NotifyValue{PID: 0, UID: -1}, nil
	}
	//: the socket closed, or failed.
	if err != nil {
		//: the listener's error.
		return coresvc.NotifyValue{}, err
	}
	//: the assignments, the kernel-named sender and the descriptors.
	return notifyValue(&v), nil
}

// notifyValue maps a received datagram to the engine's message: the
// descriptors it carried, in the order the engine reads them — MAINPIDFD=1's
// pidfd first, whose PID becomes the MAINPID= the engine reads, then
// FDSTORE=1's or BARRIER=1's —, every one the engine's to keep or close. The
// sender's pidfd is no descriptor the message carried (SCM_PIDFD): it is
// closed here, the engine naming the sender by its PID.
func notifyValue(v *coreproc.NotificationValue) coresvc.NotifyValue {
	var files []*os.File
	//: the descriptors the datagram brought.
	if d := v.Descriptors; d != nil {
		//: the main process, by the pidfd's PID.
		if d.MainPIDFD != nil {
			v.State["MAINPID"] = strconv.Itoa(v.MainPID)
			files = append(files, d.MainPIDFD)
		}
		files = append(files, d.Files...)
		//: the sender's pidfd, unused.
		if d.SenderPIDFD != nil {
			gone(d.SenderPIDFD.Close())
		}
	}
	//: what the engine reads.
	return coresvc.NotifyValue{
		Fields:   v.State,
		Files:    files,
		PID:      v.SenderPID,
		UID:      v.SenderUID,
		Verified: v.SenderPID > 0,
	}
}

// Close releases the socket and ends a Recv in flight.
func (s *listenerSource) Close() error {
	//: the listener's close.
	return s.l.Close()
}
