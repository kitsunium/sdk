// Package server — the sockets the tests stand up, on every platform.
package server

import (
	"errors"
	stdnet "net"
	"os"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// onWindows returns code when the suite runs on Windows and zero elsewhere —
// for a case whose outcome is a refusal on that platform alone. It reads
// runtime.GOOS rather than sharing the production file's build tag on
// purpose: the table is a second opinion, and it fails when someone widens or
// narrows the tag without saying so here.
func onWindows(code errs.Code) errs.Code {
	//: the platform whose answer differs.
	if runtime.GOOS == "windows" {
		return code
	}
	//: zero means "the bind must succeed" in every table that uses this.
	return 0
}

// udpPair opens a bound receiver and a sender that can reach it.
func udpPair(t *testing.T) (receiver *stdnet.UDPConn, sender *stdnet.UDPConn) {
	t.Helper()
	raw, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	receiver, ok := raw.(*stdnet.UDPConn)
	if !ok {
		t.Fatalf("ListenPacket returned a %T, want *net.UDPConn", raw)
	}
	t.Cleanup(func() {
		if cerr := receiver.Close(); cerr != nil && !errors.Is(cerr, stdnet.ErrClosed) {
			t.Errorf("close receiver: %v", cerr)
		}
	})
	addr, ok := receiver.LocalAddr().(*stdnet.UDPAddr)
	if !ok {
		t.Fatalf("LocalAddr is a %T, want *net.UDPAddr", receiver.LocalAddr())
	}
	sender, derr := stdnet.DialUDP("udp", nil, addr)
	if derr != nil {
		t.Fatalf("dial: %v", derr)
	}
	t.Cleanup(func() {
		if cerr := sender.Close(); cerr != nil {
			t.Errorf("close sender: %v", cerr)
		}
	})
	return receiver, sender
}

// releaseOnCleanup gives a descriptor back when the test ends.
//
// The file is a PARAMETER rather than a captured loop variable: capturing would
// heap-escape it on every iteration, and the case under test may or may not
// consume the descriptor, so the close is best-effort either way.
func releaseOnCleanup(t *testing.T, file *os.File) {
	t.Helper()
	t.Cleanup(func() {
		//: a descriptor the adoption already consumed is the expected case.
		swallowErr(file.Close())
	})
}

// socketDir returns a directory for the Unix sockets a test binds, removed when
// the test ends. Not t.TempDir(): that path carries the test's name, and on
// macOS $TMPDIR is already long — a socket path past the 104 bytes its sun_path
// holds (108 on Linux) fails to bind with EINVAL.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sock")
	if err != nil {
		t.Fatalf("socket directory: %v", err)
	}
	t.Cleanup(func() {
		if rerr := os.RemoveAll(dir); rerr != nil {
			t.Errorf("remove socket directory: %v", rerr)
		}
	})
	return dir
}
