//go:build windows

package ipc_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/ipc"
)

// procHandleCount is kernel32's GetProcessHandleCount, which the syscall
// package does not export.
var procHandleCount = syscall.NewLazyDLL("kernel32.dll").NewProc("GetProcessHandleCount")

// handleCount is how many handles this process holds.
func handleCount(t *testing.T) int {
	t.Helper()
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	var n uint32
	if r, _, err := procHandleCount.Call(uintptr(self), uintptr(unsafe.Pointer(&n))); r == 0 {
		t.Fatal(err)
	}
	return int(n)
}

// connectedPair dials a fresh pipe and returns both ends: the dialer's and
// the listener's.
//
// Goroutine lifecycle: one goroutine accepts once and reports on a buffered
// channel, which this function reads.
func connectedPair(t *testing.T) (client, server *coreipc.Conn) {
	t.Helper()
	cfg := &ipc.Config{Path: filepath.Join(t.TempDir(), "files.sock")}
	ln, err := ipc.NewListener(cfg)
	if err != nil {
		t.Fatal(err)
	}
	closeOrFail(t, ln)
	accepted := make(chan *coreipc.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	client, err = ipc.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	closeOrFail(t, client)
	server = <-accepted
	if server == nil {
		t.Fatal("Accept failed")
	}
	closeOrFail(t, server)
	return client, server
}

// TestPassFilesOverAPipe pins #936 on Windows, both ends in this process: a
// file passed arrives readable at the other end after the sender closed its
// own; a sender of 64 files against max=4 hands four over and leaves this
// process's handle count where it was once they are closed — the other
// sixty were closed in the sender —; a frame needs a payload and a positive
// max.
func TestPassFilesOverAPipe(t *testing.T) {
	client, server := connectedPair(t)
	sender, ok := client.Conn.(coreipc.FileSender)
	receiver, rok := server.Conn.(coreipc.FileReceiver)
	if !ok || !rok {
		t.Fatal("a pipe connection offers no FileSender / FileReceiver")
	}
	path := filepath.Join(t.TempDir(), "passed")
	if err := os.WriteFile(path, []byte("handed over"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.SendFiles([]byte("one"), f); err != nil {
		t.Fatalf("SendFiles = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, files, err := receiver.RecvFiles(buf, 4)
	if err != nil || string(buf[:n]) != "one" || len(files) != 1 {
		t.Fatalf("RecvFiles = %d %q, %d files, %v", n, buf[:n], len(files), err)
	}
	got := make([]byte, 32)
	m, rerr := files[0].Read(got)
	if rerr != nil || string(got[:m]) != "handed over" {
		t.Fatalf("the passed file reads %q, %v", got[:m], rerr)
	}
	if err := files[0].Close(); err != nil {
		t.Fatal(err)
	}

	before := handleCount(t)
	many := make([]*os.File, 64)
	for i := range many {
		if many[i], err = os.Open(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := sender.SendFiles([]byte("many"), many...); err != nil {
		t.Fatalf("SendFiles(64) = %v", err)
	}
	for _, m := range many {
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
	}
	n, files, err = receiver.RecvFiles(buf, 4)
	if len(files) != 4 || string(buf[:n]) != "many" || !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Fatalf("RecvFiles(max 4) = %q, %d files, %v; want four and FD_PASS_REFUSED truncated", buf[:n], len(files), err)
	}
	for _, f := range files {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// The runtime may open a handle or two meanwhile (a thread); sixty left
	// behind would not hide in that margin.
	if after := handleCount(t); after > before+8 {
		t.Fatalf("%d handles after, %d before: the excess was not closed", after, before)
	}

	if err := sender.SendFiles(nil, many[0]); !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Errorf("SendFiles with no payload = %v, want FD_PASS_REFUSED", err)
	}
	if _, _, err := receiver.RecvFiles(buf, 0); !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Errorf("RecvFiles(max 0) = %v, want FD_PASS_REFUSED", err)
	}
}
