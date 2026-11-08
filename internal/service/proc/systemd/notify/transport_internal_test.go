//go:build linux || freebsd

// Package notify — the sd_notify transport end to end on a real socket (#730),
// on Linux and FreeBSD: descriptors, the barrier, the sender's identity. What
// differs — counting descriptors, the credentials' size, a pidfd — is in the
// transport_<os>_internal_test.go beside this one.
package notify

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// testListener stands up a listener closed at the test's end.
func testListener(t *testing.T) (*listener, string) {
	t.Helper()
	l, path, err := Listen()
	if err != nil {
		t.Fatalf("Listen = %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	ln, ok := l.(*listener)
	if !ok {
		t.Fatalf("Listen returned %T", l)
	}
	return ln, path
}

// sendWith sends body to the socket at path with fds passed (SCM_RIGHTS).
func sendWith(t *testing.T, path, body string, fds ...int) {
	t.Helper()
	sock, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	defer func() { _ = syscall.Close(sock) }()
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	if err = syscall.Sendmsg(sock, []byte(body), oob, &syscall.SockaddrUnix{Name: path}, 0); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// pipes opens n pipes, closed at the test's end, and returns their read ends.
func pipes(t *testing.T, n int) ([]*os.File, []int) {
	t.Helper()
	files := make([]*os.File, 0, n)
	fds := make([]int, 0, n)
	for range n {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
		files = append(files, r)
		fds = append(fds, int(r.Fd()))
	}
	return files, fds
}

// sameFile reports whether a and b are the same open file's inode.
func sameFile(t *testing.T, a, b *os.File) bool {
	t.Helper()
	ia, err := a.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	ib, err := b.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return os.SameFile(ia, ib)
}

// TestRecvFDStore pins SCM_RIGHTS with FDSTORE=1: every descriptor arrives,
// close-on-exec, the very file the sender passed, and the caller's.
func TestRecvFDStore(t *testing.T) {
	t.Parallel()
	l, path := testListener(t)
	sent, fds := pipes(t, 2)
	sendWith(t, path, "FDSTORE=1\nFDNAME=web\n", fds...)
	v, err := l.Recv()
	if err != nil {
		t.Fatalf("Recv = %v", err)
	}
	if len(filesOf(v)) != 2 || v.State["FDNAME"] != "web" {
		t.Fatalf("Recv = %d files, state %v, want 2 and FDNAME=web", len(filesOf(v)), v.State)
	}
	for i, f := range filesOf(v) {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFD, 0)
		if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
			t.Errorf("file %d arrived without close-on-exec (%#x, %v)", i, flags, errno)
		}
		if !sameFile(t, f, sent[i]) {
			t.Errorf("file %d is not the pipe sent in its place", i)
		}
		_ = f.Close()
	}
}

// TestRecvClosesWhatNothingCalledFor pins systemd's rule: descriptors that
// come with no assignment calling for them are closed, the datagram read.
func TestRecvClosesWhatNothingCalledFor(t *testing.T) {
	l, path := testListener(t)
	_, fds := pipes(t, 3)
	before := openFDs(t)
	sendWith(t, path, "READY=1\n", fds...)
	v, err := l.Recv()
	if err != nil || !v.Ready() || len(filesOf(v)) != 0 {
		t.Fatalf("Recv = %+v, %v, want READY=1 and no file", v, err)
	}
	closeSender(v)
	if after := openFDs(t); after != before {
		t.Errorf("descriptors %d before, %d after: the uncalled-for ones leaked", before, after)
	}
}

// closeSender closes the sender's pidfd a value may carry.
func closeSender(v coreproc.NotificationValue) {
	if senderPIDFDOf(v) != nil {
		_ = senderPIDFDOf(v).Close()
	}
}

// TestRecvRefusalClosesEverything pins #730's leak criterion: a datagram
// refused — an assignment out of bounds, a truncated control buffer, a barrier
// without its one descriptor — leaves none of its descriptors open.
func TestRecvRefusalClosesEverything(t *testing.T) {
	l, path := testListener(t)
	type tc struct {
		name, body string
		fds        int
		//: a control buffer this small, 0 for the full one.
		oob int
	}
	tests := []tc{
		{name: "an assignment out of bounds", body: "READY=2\nFDSTORE=1\n", fds: 3},
		{name: "a truncated control buffer", body: "FDSTORE=1\n", fds: truncatingFDs, oob: truncatedOOB()},
		{name: "a barrier without a descriptor", body: "BARRIER=1\n"},
		{name: "a barrier with two", body: "BARRIER=1\n", fds: 2},
		{name: "a main pidfd that is a pipe", body: "MAINPIDFD=1\n", fds: 1},
		{name: "an embedded NUL", body: "READY=1\n\x00STATUS=x\n", fds: 2},
	}
	for _, c := range tests {
		_, fds := pipes(t, c.fds)
		before := openFDs(t)
		sendWith(t, path, c.body, fds...)
		oob := c.oob
		if oob == 0 {
			oob = oobBufSize()
		}
		_, err := l.recv(oob)
		if !errs.HasCode(err, coreproc.CodeInvalidNotification) {
			t.Errorf("%s: Recv = %v, want INVALID_NOTIFICATION", c.name, err)
		}
		if after := openFDs(t); after != before {
			t.Errorf("%s: descriptors %d before, %d after the refusal", c.name, before, after)
		}
	}
}

// TestRecvBarrier pins BARRIER=1: one descriptor, returned alone with its
// datagram's other assignments dropped; the sender sees the barrier pass when
// the caller closes it, and not before.
func TestRecvBarrier(t *testing.T) {
	t.Parallel()
	l, path := testListener(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	sendWith(t, path, "BARRIER=1\nREADY=1\n", int(w.Fd()))
	_ = w.Close()
	v, err := l.Recv()
	if err != nil {
		t.Fatalf("Recv = %v", err)
	}
	if len(v.State) != 1 || v.State["BARRIER"] != "1" || len(filesOf(v)) != 1 {
		t.Fatalf("Recv = state %v, %d files; want BARRIER=1 alone with its descriptor", v.State, len(filesOf(v)))
	}
	_ = r.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, rErr := r.Read(make([]byte, 1)); !errors.Is(rErr, os.ErrDeadlineExceeded) {
		t.Fatalf("the barrier passed before the caller closed it: %v", rErr)
	}
	_ = filesOf(v)[0].Close()
	_ = r.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, rErr := r.Read(make([]byte, 1)); rErr == nil || errors.Is(rErr, os.ErrDeadlineExceeded) {
		t.Errorf("after the close the sender reads %v, want EOF", rErr)
	}
}

// TestRecvSenderIdentity pins the identity #730 exposes for the calling layer
// to judge: the kernel's PID, user and group, and a pidfd on the sender where
// the kernel attaches one (Linux 6.5).
func TestRecvSenderIdentity(t *testing.T) {
	t.Parallel()
	l, path := testListener(t)
	sendWith(t, path, "READY=1\n")
	v, err := l.Recv()
	if err != nil {
		t.Fatalf("Recv = %v", err)
	}
	defer closeSender(v)
	if v.SenderPID != os.Getpid() || v.SenderUID != os.Getuid() || v.SenderGID != os.Getgid() {
		t.Errorf("sender %d/%d/%d, want %d/%d/%d", v.SenderPID, v.SenderUID, v.SenderGID, os.Getpid(), os.Getuid(), os.Getgid())
	}
	if senderPIDFDOf(v) == nil {
		t.Log("no SCM_PIDFD: a kernel before 6.5")
		return
	}
	pid, pErr := pidfdPID(int(senderPIDFDOf(v).Fd()))
	if pErr != nil || pid != os.Getpid() {
		t.Errorf("the sender's pidfd names %d (%v), want %d", pid, pErr, os.Getpid())
	}
}

// TestRecvNoLeakOverCycles pins #730's leak test: descriptors stored, then
// closed by the caller, N times, leave the table as it was.
func TestRecvNoLeakOverCycles(t *testing.T) {
	l, path := testListener(t)
	_, fds := pipes(t, 2)
	before := openFDs(t)
	for range 300 {
		sendWith(t, path, "FDSTORE=1\n", fds...)
		v, err := l.Recv()
		if err != nil {
			t.Fatalf("Recv = %v", err)
		}
		for _, f := range filesOf(v) {
			_ = f.Close()
		}
		closeSender(v)
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors %d before, %d after 300 cycles", before, after)
	}
}

// TestListenFor pins the per-service socket: handed to the caller's own user
// it works; handed to another without the privilege it is LISTEN_FAILED and
// leaves no directory behind.
func TestListenFor(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	l, path, err := ListenFor(os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatalf("ListenFor(own user) = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != os.Getuid() {
		t.Errorf("the socket belongs to %d, want %d", st.Uid, os.Getuid())
	}
	_ = l.Close()
	if os.Geteuid() == 0 {
		t.Skip("root may give a socket away: the refusal needs an unprivileged run")
	}
	if l2, _, err2 := ListenFor(os.Getuid()+12345, os.Getgid()); l2 != nil || !errs.HasCode(err2, coreproc.CodeListenFailed) {
		t.Fatalf("ListenFor(another user) = %v, %v, want LISTEN_FAILED", l2, err2)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	if len(names) != 0 {
		t.Errorf("left behind in TMPDIR: %s", strings.Join(names, ", "))
	}
}

// filesOf returns the descriptors v carried, nil when none.
func filesOf(v coreproc.NotificationValue) []*os.File {
	if v.Descriptors == nil {
		return nil
	}
	return v.Descriptors.Files
}

// senderPIDFDOf returns the pidfd on v's sender, nil when none.
func senderPIDFDOf(v coreproc.NotificationValue) *os.File {
	if v.Descriptors == nil {
		return nil
	}
	return v.Descriptors.SenderPIDFD
}

// mainPIDFDOf returns the MAINPIDFD=1 pidfd v carried, nil when none.
func mainPIDFDOf(v coreproc.NotificationValue) *os.File {
	if v.Descriptors == nil {
		return nil
	}
	return v.Descriptors.MainPIDFD
}
