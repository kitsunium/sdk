//go:build windows

package ipc

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
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

// TestAFrameOfAnUnknownKindIsRefused pins the second frame layout (#936):
// every entry is tagged, a handle or a socket, and an entry of any other kind
// ends the frame — what follows it cannot be read — with FD_PASS_REFUSED;
// so does a frame whose magic is neither layout's.
func TestAFrameOfAnUnknownKindIsRefused(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	reader := &pipeConn{File: r, peer: coreipc.PeerValue{Verified: true}, proc: syscall.InvalidHandle}
	frame := binary.LittleEndian.AppendUint32(nil, fileFrameMagic)
	frame = binary.LittleEndian.AppendUint32(frame, 1)
	frame = binary.LittleEndian.AppendUint32(frame, 1)
	frame = binary.LittleEndian.AppendUint32(frame, 9)
	if _, err := w.Write(frame); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.RecvFiles(make([]byte, 8), 1); !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Fatalf("RecvFiles on an entry of kind 9 = %v, want FD_PASS_REFUSED", err)
	}
	unknown := binary.LittleEndian.AppendUint32(nil, 0x334B4453) // "SDK3"
	unknown = binary.LittleEndian.AppendUint32(unknown, 0)
	unknown = binary.LittleEndian.AppendUint32(unknown, 1)
	if _, err := w.Write(append(unknown, 'x')); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.RecvFiles(make([]byte, 8), 1); !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Fatalf("RecvFiles on an unknown magic = %v, want FD_PASS_REFUSED", err)
	}
}

// TestAFirstLayoutFrameIsStillRead pins the version rule (ADR 0148, amendment
// 2026-11-11): a frame laid out as v0.60.0 (#1316) writes it — "SDKF", the
// count, the payload's length, bare handle values in the sender's table, the
// payload — is still read, and its file taken out of the sender's process.
// The sender here is this process, held by a real handle as the connection
// holds its peer's.
func TestAFirstLayoutFrameIsStillRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passed")
	if err := os.WriteFile(path, []byte("from v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	//: the sender's half, as #1316 wrote it: a private duplicate in its table.
	var dup syscall.Handle
	if err := syscall.DuplicateHandle(self, syscall.Handle(f.Fd()), self, &dup, 0, false, duplicateSameAccess); err != nil {
		t.Fatal(err)
	}
	peer, err := syscall.OpenProcess(processDupHandle, false, uint32(os.Getpid()))
	if err != nil {
		closeHandles(dup)
		t.Fatal(err)
	}
	defer closeHandles(peer)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	frame := binary.LittleEndian.AppendUint32(nil, fileFrameMagicV1)
	frame = binary.LittleEndian.AppendUint32(frame, 1)
	frame = binary.LittleEndian.AppendUint32(frame, 2)
	frame = binary.LittleEndian.AppendUint64(frame, uint64(dup))
	if _, err := w.Write(append(frame, "v1"...)); err != nil {
		t.Fatal(err)
	}
	reader := &pipeConn{File: r, peer: coreipc.PeerValue{Verified: true}, proc: peer}
	buf := make([]byte, 8)
	n, files, err := reader.RecvFiles(buf, 1)
	if err != nil || string(buf[:n]) != "v1" || len(files) != 1 {
		t.Fatalf("RecvFiles on a first-layout frame = %q, %d files, %v; want the payload and one file", buf[:n], len(files), err)
	}
	defer files[0].Close()
	got := make([]byte, 16)
	if m, rerr := files[0].Read(got); rerr != nil || string(got[:m]) != "from v1" {
		t.Fatalf("the file read %q, %v; want from v1", got[:m], rerr)
	}
}
