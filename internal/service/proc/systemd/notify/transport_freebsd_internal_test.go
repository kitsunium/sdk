//go:build freebsd

// Package notify — what the shared transport tests need of FreeBSD, and the
// tests of its own credential passing (#730): LOCAL_CREDS_PERSISTENT set,
// SCM_CREDS2 read with every datagram, a process descriptor's PID. They run on
// the e2e-cross FreeBSD lane only; the authoring host never ran them.
package notify

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxFDProbe bounds the descriptors openFDs probes.
const maxFDProbe int = 4096

// truncatingFDs is how many descriptors overflow truncatedOOB whatever the
// sender's group count: SCM_CREDS2 takes 28 to 92 bytes, so between 2 and 18
// descriptors fit beside it.
const truncatingFDs int = 40

// truncatedOOB is a control buffer with room for the largest SCM_CREDS2 and
// two descriptors: the kernel installs what fits, then sets MSG_CTRUNC.
func truncatedOOB() int {
	return syscall.CmsgSpace(sockcred2Max) + syscall.CmsgSpace(2*fdBytes)
}

// openFDs counts this process's descriptors by fstat(2): /dev/fd lists only 0
// to 2 without fdescfs.
func openFDs(t *testing.T) int {
	t.Helper()
	n := 0
	var st syscall.Stat_t
	for fd := range maxFDProbe {
		if syscall.Fstat(fd, &st) == nil {
			n++
		}
	}
	return n
}

// TestEnableCredsFreeBSD pins the option every credential check rests on:
// LOCAL_CREDS_PERSISTENT reads back set on a fresh socket, and a datagram sent
// to it carries this process as its sender — PID, real uid, real gid —, read
// from SCM_CREDS2 by bsdabi.Sockcred2.
func TestEnableCredsFreeBSD(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "n.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err = enableCreds(conn); err != nil {
		t.Fatalf("enableCreds = %v", err)
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var value int
	var optErr error
	if cErr := raw.Control(func(fd uintptr) {
		value, optErr = syscall.GetsockoptInt(int(fd), solLocal, localCredsPersistent)
	}); cErr != nil || optErr != nil || value == 0 {
		t.Fatalf("LOCAL_CREDS_PERSISTENT = %d (%v, %v), want set", value, cErr, optErr)
	}
	l := &listener{conn: conn}
	for range 2 {
		sendWith(t, path, "READY=1\n")
		v, rErr := l.Recv()
		if rErr != nil {
			t.Fatalf("Recv = %v", rErr)
		}
		if v.SenderPID != os.Getpid() || v.SenderUID != os.Getuid() || v.SenderGID != os.Getgid() {
			t.Errorf("sender %d/%d/%d, want %d/%d/%d", v.SenderPID, v.SenderUID, v.SenderGID, os.Getpid(), os.Getuid(), os.Getgid())
		}
	}
}

// TestRecvWithoutCredentialsFreeBSD pins the refusal of a datagram the kernel
// stamped with no sender: a socket without LOCAL_CREDS_PERSISTENT delivers
// none, and Recv answers CREDENTIAL_MISMATCH with the descriptors it carried
// closed.
func TestRecvWithoutCredentialsFreeBSD(t *testing.T) { // not parallel: openFDs counts the whole process
	path := filepath.Join(t.TempDir(), "n.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	defer func() { _ = conn.Close() }()
	l := &listener{conn: conn}
	_, fds := pipes(t, 2)
	before := openFDs(t)
	sendWith(t, path, "FDSTORE=1\n", fds...)
	if _, rErr := l.Recv(); !errs.HasCode(rErr, coreproc.CodeCredentialMismatch) {
		t.Errorf("Recv without credentials = %v, want CREDENTIAL_MISMATCH", rErr)
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors %d before, %d after the refusal", before, after)
	}
}

// TestPidfdPIDFreeBSD pins pdgetpid(2)'s refusal of a descriptor that is no
// process descriptor: MAINPIDFD=1 with a pipe is refused (the shared
// TestRecvRefusalClosesEverything sends one). A process descriptor itself
// comes only from pdfork(2), which the stdlib does not reach, so the
// acceptance is not tested.
func TestPidfdPIDFreeBSD(t *testing.T) {
	t.Parallel()
	r, _ := pipes(t, 1)
	if pid, err := pidfdPID(int(r[0].Fd())); err == nil {
		t.Errorf("pidfdPID(a pipe) = %d, nil; want a refusal", pid)
	}
}
