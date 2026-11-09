//go:build linux

package ipc

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// openFDs counts this process's open descriptors.
func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries) - 1
}

// TestPassFiles pins #936 over a private socket under the test's own
// directory: a file passed arrives readable at the other end; a sender of 64
// descriptors against max=4 leaves the process's descriptor count where it was
// once the four are closed — the kernel closed the other sixty; and an
// unnamed peer is FD_PASS_REFUSED with nothing written. Not parallel: it
// counts the process's descriptors.
func TestPassFiles(t *testing.T) {
	client, server := filePair(t)
	sender, ok := client.Conn.(coreipc.FileSender)
	receiver, rok := server.Conn.(coreipc.FileReceiver)
	if !ok || !rok {
		t.Fatal("a Unix connection offers no FileSender / FileReceiver")
	}
	passed := filepath.Join(t.TempDir(), "passed")
	if err := os.WriteFile(passed, []byte("through the socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(passed)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.SendFiles([]byte("x"), f); err != nil {
		t.Fatalf("SendFiles = %v", err)
	}
	_ = f.Close()
	buf := make([]byte, 8)
	n, files, err := receiver.RecvFiles(buf, 4)
	if err != nil || n != 1 || len(files) != 1 {
		t.Fatalf("RecvFiles = %d, %d files, %v", n, len(files), err)
	}
	got := make([]byte, 64)
	m, _ := files[0].ReadAt(got, 0)
	_ = files[0].Close()
	if string(got[:m]) != "through the socket" {
		t.Fatalf("the passed file reads %q", got[:m])
	}
	before := openFDs(t)
	many := make([]*os.File, 0, 64)
	for range 64 {
		g, oerr := os.Open(passed)
		if oerr != nil {
			t.Fatal(oerr)
		}
		many = append(many, g)
	}
	if err := sender.SendFiles([]byte("y"), many...); err != nil {
		t.Fatalf("SendFiles(64) = %v", err)
	}
	for _, g := range many {
		_ = g.Close()
	}
	_, files, err = receiver.RecvFiles(buf, 4)
	if len(files) != 4 || !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Fatalf("RecvFiles(max 4) of 64 = %d files, %v; want 4 and the truncation said", len(files), err)
	}
	for _, g := range files {
		_ = g.Close()
	}
	if after := openFDs(t); after != before {
		t.Fatalf("%d descriptors after, %d before: the excess leaked", after, before)
	}
	if _, _, err := receiver.RecvFiles(buf, 0); !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Fatalf("RecvFiles(max 0) = %v, want FD_PASS_REFUSED", err)
	}
	unnamed := &fileConn{UnixConn: client.Conn.(*fileConn).UnixConn}
	if err := unnamed.SendFiles([]byte("z"), many[0]); !errs.HasCode(err, coreipc.CodeFDPassRefused) {
		t.Fatalf("SendFiles to an unnamed peer = %v, want FD_PASS_REFUSED", err)
	}
	_ = server.Conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, rerr := server.Conn.Read(buf); rerr == nil {
		t.Fatal("the refused send wrote something")
	}
}
