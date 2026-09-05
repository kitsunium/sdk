//go:build linux

// Package sdnotify — the supervisor-side listener.
//
// Everything here exists to make one guarantee: a notification is attributed to
// the process the KERNEL says sent it, not to whatever the datagram claims. A
// notify socket is reachable by anything that can find the path, so without that
// guarantee any local process could declare a service ready, or tell the
// supervisor to track a different pid as its main process.
package sdnotify

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_wrapListen pins the socket-setup wrapper, including the path it names —
// the listener binds under a private temp directory, so the path is the only way
// an operator learns where it tried.
func Test_wrapListen(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		cause error
		path  string
	}
	tests := []tc{
		{"a bind failure", os.ErrPermission, "/tmp/sdnotify-x/notify"},
		{"a directory failure with no path yet", os.ErrPermission, ""},
		{"a nil cause", nil, "/tmp/sdnotify-x/notify"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := wrapListen(c.cause, c.path)

		if !errs.HasCode(err, coreproc.CodeListenFailed) {
			t.Fatalf("wrapListen = %v, want LISTEN_FAILED", err)
		}
		//: EX_OSERR: the operating system refused, not the caller.
		if got := errs.ExitCodeOf(err); got != exitOSErr {
			t.Errorf("exit code = %d, want %d", got, exitOSErr)
		}
		var named bool
		for _, f := range errs.FieldsOf(err) {
			if f.Key() == "socket" && f.StringValue() == c.path {
				named = true
			}
		}
		if !named {
			t.Errorf("the error does not name the socket: %v", errs.FieldsOf(err))
		}
		if c.cause != nil && !errors.Is(err, c.cause) {
			t.Errorf("wrapListen = %v, want it to wrap %v", err, c.cause)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapCredMismatch pins the sentinel for an unattributable datagram. It is
// deliberately NOT a malformed-notification error: the body may be perfectly
// well-formed, and what failed is the identity check — so it carries EX_NOPERM,
// which is what tells a supervisor this was a permission problem rather than a
// protocol one.
func Test_wrapCredMismatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"an unattributable datagram"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := wrapCredMismatch()

		if !errs.HasCode(err, coreproc.CodeCredentialMismatch) {
			t.Fatalf("wrapCredMismatch = %v, want CREDENTIAL_MISMATCH", err)
		}
		//: EX_NOPERM, not EX_DATAERR: the datagram may be well-formed.
		if got := errs.ExitCodeOf(err); got != exitNoPerm {
			t.Errorf("exit code = %d, want %d", got, exitNoPerm)
		}
		if reason, _ := errs.ReasonOf(err); reason != "CREDENTIAL_MISMATCH" {
			t.Errorf("reason = %q, want CREDENTIAL_MISMATCH", reason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_senderPID pins the "no credentials means NO pid" rule, which is the whole
// security property.
//
// Returning (0, true) for an unparseable control message would let an
// unattributable datagram through as if pid 0 had sent it — and pid 0 is a value
// a caller comparing against a known child pid would simply find unequal, or
// worse, treat as "the kernel said so". The false is what makes Recv reject it.
func Test_senderPID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		oob  []byte
		//: whether a credential must be found.
		wantOK bool
	}
	//: a real SCM_CREDENTIALS message for this process.
	valid := syscall.UnixCredentials(&syscall.Ucred{
		Pid: int32(os.Getpid()),
		Uid: uint32(os.Getuid()),
		Gid: uint32(os.Getgid()),
	})
	tests := []tc{
		{name: "no control data at all", oob: nil},
		{name: "an empty control buffer", oob: []byte{}},
		{name: "junk that is not a control message", oob: []byte{0xFF, 0xFF, 0xFF, 0xFF}},
		{name: "a truncated control message", oob: valid[:len(valid)/2]},
		{name: "a real credential message", oob: valid, wantOK: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pid, ok := senderPID(c.oob)

		if ok != c.wantOK {
			t.Fatalf("senderPID(%s) ok = %v, want %v", c.name, ok, c.wantOK)
		}
		if !ok {
			//: a rejected credential must report NO pid, or a caller checking
			//: only the value would attribute the datagram to pid 0.
			if pid != 0 {
				t.Errorf("senderPID(%s) returned pid %d beside the rejection", c.name, pid)
			}
			return
		}
		//: the kernel stamps the real sender, which here is this process.
		if pid != os.Getpid() {
			t.Errorf("senderPID = %d, want %d", pid, os.Getpid())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_enablePasscred pins the setsockopt that makes every later credential
// check possible. Without SO_PASSCRED the kernel attaches no ucred at all, so
// senderPID would find nothing and Recv would reject every datagram — the
// listener would look broken rather than insecure, which is the better failure
// but still a failure.
func Test_enablePasscred(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a freshly bound datagram socket"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "n.sock")
		conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
		if err != nil {
			t.Fatalf("binding: %v", err)
		}
		defer func() {
			if cerr := conn.Close(); cerr != nil {
				t.Logf("closing: %v", cerr)
			}
		}()

		if perr := enablePasscred(conn); perr != nil {
			t.Fatalf("enablePasscred = %v, want nil", perr)
		}

		//: read the option back: it is what the kernel consults when it decides
		//: whether to attach a ucred to each datagram.
		raw, rerr := conn.SyscallConn()
		if rerr != nil {
			t.Fatalf("SyscallConn: %v", rerr)
		}
		var value int
		var optErr error
		if cerr := raw.Control(func(fd uintptr) {
			value, optErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED)
		}); cerr != nil {
			t.Fatalf("Control: %v", cerr)
		}
		if optErr != nil {
			t.Fatalf("reading SO_PASSCRED: %v", optErr)
		}
		if value == 0 {
			t.Error("SO_PASSCRED is off, so no datagram would carry credentials")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_listener_Recv pins the receive path end to end against a real socket.
//
// The truncation check is the one worth calling out. A datagram longer than the
// buffer arrives CLIPPED, and a clipped environment block still parses — it just
// silently loses whatever fields fell off the end. Rejecting it is the only way
// to avoid acting on half a notification.
func Test_listener_Recv(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the datagram body a client sends.
		body string
		//: the expected outcome.
		wantCode  errs.Code
		wantState map[string]string
		wantPID   bool
	}
	tests := []tc{
		{
			name:      "a readiness notification",
			body:      "READY=1\n",
			wantState: map[string]string{"READY": "1"},
			wantPID:   true,
		},
		{
			name:      "several fields",
			body:      "READY=1\nSTATUS=serving\n",
			wantState: map[string]string{"READY": "1", "STATUS": "serving"},
			wantPID:   true,
		},
		{
			name:     "a line with no equals sign",
			body:     "READY\n",
			wantCode: coreproc.CodeInvalidNotification,
		},
		{
			name:     "a non-numeric main pid",
			body:     "MAINPID=not-a-pid\n",
			wantCode: coreproc.CodeInvalidNotification,
		},
		{
			//: longer than the receive buffer: the kernel clips it and sets
			//: MSG_TRUNC, and a clipped environment block still parses.
			name:     "a datagram larger than the buffer",
			body:     "STATUS=" + strings.Repeat("x", payloadBufSize*2) + "\n",
			wantCode: coreproc.CodeInvalidNotification,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l, socketPath, err := Listen()
		if err != nil {
			t.Fatalf("Listen = %v, want nil", err)
		}
		defer func() {
			if cerr := l.Close(); cerr != nil {
				t.Logf("closing the listener: %v", cerr)
			}
		}()

		//: send from this process, so the kernel-stamped pid is one we know.
		conn, derr := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socketPath, Net: "unixgram"})
		if derr != nil {
			t.Fatalf("dialling: %v", derr)
		}
		if _, werr := conn.Write([]byte(c.body)); werr != nil {
			t.Fatalf("sending: %v", werr)
		}
		if cerr := conn.Close(); cerr != nil {
			t.Logf("closing the client: %v", cerr)
		}

		got, rerr := l.Recv()

		if c.wantCode != 0 {
			if !errs.HasCode(rerr, c.wantCode) {
				t.Fatalf("Recv = %v, want code %v", rerr, c.wantCode)
			}
			return
		}
		if rerr != nil {
			t.Fatalf("Recv = %v, want nil", rerr)
		}
		for name, want := range c.wantState {
			if got.State[name] != want {
				t.Errorf("State[%q] = %q, want %q", name, got.State[name], want)
			}
		}
		//: the pid is the KERNEL's, not the datagram's — that is the entire
		//: point of SO_PASSCRED.
		if c.wantPID && got.SenderPID != os.Getpid() {
			t.Errorf("SenderPID = %d, want %d", got.SenderPID, os.Getpid())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_listener_Close pins the teardown: the socket goes, the private temp
// directory goes with it, and a blocked Recv is released.
//
// The unblock matters because a supervisor shutting down has a goroutine parked
// in Recv, and a Close that did not release it would hang the shutdown on a
// datagram that is never coming.
func Test_listener_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: whether a Recv is parked when Close is called.
		blocked bool
	}
	tests := []tc{
		{"an idle listener", false},
		{"a listener with a blocked Recv", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l, socketPath, err := Listen()
		if err != nil {
			t.Fatalf("Listen = %v, want nil", err)
		}

		//: Goroutine lifecycle: at most one goroutine parked in Recv. Close
		//: below unblocks it, and the receive on done joins it before the
		//: assertions, so it cannot outlive this case.
		done := make(chan error, 1)
		if c.blocked {
			go func() {
				_, rerr := l.Recv()
				done <- rerr
			}()
			//: give it a moment to actually park in the read.
			time.Sleep(50 * time.Millisecond)
		}

		if cerr := l.Close(); cerr != nil {
			t.Fatalf("Close = %v, want nil", cerr)
		}

		if c.blocked {
			select {
			case rerr := <-done:
				//: the parked Recv is released with the typed listen failure
				//: rather than hanging forever.
				if !errs.HasCode(rerr, coreproc.CodeListenFailed) {
					t.Errorf("the parked Recv = %v, want LISTEN_FAILED", rerr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Close did not release the parked Recv")
			}
		}

		//: the socket inode and its private directory are gone, so nothing is
		//: left behind in the temp filesystem.
		if _, serr := os.Stat(socketPath); serr == nil {
			t.Error("the socket survived Close")
		}
		if _, serr := os.Stat(filepath.Dir(socketPath)); serr == nil {
			t.Error("the private directory survived Close")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
