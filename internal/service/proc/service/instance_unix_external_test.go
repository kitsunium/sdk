//go:build unix

package service_test

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// TestRealInstances: three connections — socket pairs — three instances of
// one template over proc/exec, each process writing to its fd 3 what it was
// told: LISTEN_FDS=1, LISTEN_FDNAMES=connection, a LISTEN_PID that is its own.
// Each peer reads that much, then the end of the stream: the process gone,
// the instance ended and closed its connection.
func TestRealInstances(t *testing.T) {
	template := service.Config{
		Spec: coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin"}},
		ExecStart: []coresvc.CommandValue{command(t, ":/bin/sh", "-c",
			`printf '%s|%s|%s|%s' "$LISTEN_FDS" "$LISTEN_FDNAMES" "$LISTEN_PID" "$$" >&3`)},
	}
	peers := make([]*os.File, 3)
	for i := range peers {
		pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatal(err)
		}
		syscall.CloseOnExec(pair[0])
		syscall.CloseOnExec(pair[1])
		if err = syscall.SetNonblock(pair[1], true); err != nil {
			t.Fatal(err)
		}
		conn, peer := os.NewFile(uintptr(pair[0]), "conn"), os.NewFile(uintptr(pair[1]), "peer")
		t.Cleanup(func() { _ = peer.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		u, err := service.SpawnInstance(ctx, &template, "real-"+strconv.Itoa(i), conn)
		cancel()
		if err != nil {
			t.Fatalf("SpawnInstance %d: %v", i, err)
		}
		t.Cleanup(func() { _ = u.Close(context.Background()) })
		peers[i] = peer
	}
	for i, peer := range peers {
		if err := peer.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(peer)
		if err != nil {
			t.Fatalf("instance %d: no end of stream: %v (read %q)", i, err, raw)
		}
		got := strings.Split(string(raw), "|")
		if len(got) != 4 || got[0] != "1" || got[1] != "connection" || got[2] != got[3] || got[2] == strconv.Itoa(os.Getpid()) {
			t.Errorf("instance %d told %q, want 1|connection|its own pid|its pid", i, raw)
		}
	}
}
