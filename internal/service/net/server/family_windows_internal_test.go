//go:build windows

// Package server — the two unix socket families Windows refuses, and the
// measurement that justifies refusing them.
package server

import (
	stdnet "net"
	"os"
	"path/filepath"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestWindowsHasNoUnixDatagramOrSeqpacketSocket is the measurement
// platformLacks rests on, taken with the standard library alone: on this
// kernel neither a unixgram nor a unixpacket socket can be opened at all.
//
// It is what keeps the refusal honest. The day Windows implements either
// family this fails, and the refusal in family_windows.go becomes a wrong
// answer that must go.
func TestWindowsHasNoUnixDatagramOrSeqpacketSocket(t *testing.T) {
	t.Parallel()
	dir := socketDir(t)
	//: the kernel itself has no unix datagram socket,
	if pc, err := stdnet.ListenPacket("unixgram", filepath.Join(dir, "dgram.sock")); err == nil {
		swallowErr(pc.Close())
		t.Fatal("a unixgram socket opened on windows — AF_UNIX has datagrams now, and platformLacks must stop refusing them")
	}
	//: nor a unix seqpacket one.
	if ln, err := stdnet.Listen("unixpacket", filepath.Join(dir, "seq.sock")); err == nil {
		swallowErr(ln.Close())
		t.Fatal("a unixpacket socket opened on windows — AF_UNIX has seqpacket now, and platformLacks must stop refusing it")
	}
	//: and the stream family the engine does serve here really is served.
	ln, err := stdnet.Listen("unix", filepath.Join(dir, "stream.sock"))
	//: served, so the refusals above are about the family, not AF_UNIX.
	if err != nil {
		t.Fatalf("a unix stream socket did not open on windows: %v", err)
	}
	swallowErr(ln.Close())
}

// Test_familyUnavailable_refusesBeforeTheOSIsAsked pins the refusal on both
// engines: the SDK's UNSUPPORTED_PLATFORM naming the family, no socket handed
// back, and no socket file left behind — the proof the kernel was never asked.
func Test_familyUnavailable_refusesBeforeTheOSIsAsked(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		network string
		bind    func(t *testing.T, addr corenet.AddressValue) (bound bool, err error)
	}
	tests := []tc{
		{name: "a unix datagram socket on the datagram engine", network: "unixgram", bind: func(t *testing.T, addr corenet.AddressValue) (bool, error) {
			t.Helper()
			pc, err := listenPacket(t.Context(), addr)
			//: a socket that should not exist is still released.
			if pc != nil {
				swallowErr(pc.Close())
			}
			return pc != nil, err
		}},
		{name: "a unix seqpacket socket on the stream engine", network: "unixpacket", bind: func(t *testing.T, addr corenet.AddressValue) (bool, error) {
			t.Helper()
			ln, err := listen(t.Context(), addr, corenet.IdentityValue{}, false, false)
			//: a listener that should not exist is still released.
			if ln != nil {
				swallowErr(ln.Close())
			}
			return ln != nil, err
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(socketDir(t), "refused.sock")
		bound, err := c.bind(t, corenet.AddressValue{Network: c.network, Addr: path})
		//: the platform's refusal, by its typed code,
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("%s = %v, want UNSUPPORTED_PLATFORM", c.network, err)
		}
		//: with nothing bound behind it,
		if bound {
			t.Error("a refused family still handed back a socket")
		}
		//: and nothing on disk: the kernel was never asked.
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Errorf("the refusal left %s behind (%v) — the kernel was asked after all", path, statErr)
		}
	}
	//: one subtest per refused family.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
