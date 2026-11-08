//go:build windows

package ipc

import (
	"io"
	"os"
	"syscall"
	"testing"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestAFrameNeedsANamedPeerAndAFrame pins the refusals the pipe makes
// without a listener: SendFiles to a peer the kernel did not name writes
// nothing — the next bytes on the pipe are the test's own —, and RecvFiles
// on a stream that is not at a frame refuses it.
func TestAFrameNeedsANamedPeerAndAFrame(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	unnamed := &pipeConn{File: w, peer: coreipc.PeerValue{UID: -1, GID: -1}, proc: syscall.InvalidHandle}
	if err := unnamed.SendFiles([]byte("x"), r); !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Fatalf("SendFiles to an unnamed peer = %v, want FD_PASS_REFUSED", err)
	}
	if _, err := w.Write([]byte("plain bytes!")); err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 5)
	if _, err := io.ReadFull(r, first); err != nil || string(first) != "plain" {
		t.Fatalf("the pipe holds %q, %v: the refused send wrote", first, err)
	}
	reader := &pipeConn{File: r, peer: coreipc.PeerValue{Verified: true}, proc: syscall.InvalidHandle}
	if _, err := w.Write([]byte(" bytes, not a frame")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.RecvFiles(make([]byte, 8), 1); !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Fatalf("RecvFiles on plain bytes = %v, want FD_PASS_REFUSED", err)
	}
}
