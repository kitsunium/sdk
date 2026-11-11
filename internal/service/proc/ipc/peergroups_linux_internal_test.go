//go:build linux

package ipc

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestPeerGroupsAreTheKernels pins SO_PEERGROUPS (#1092) on both ends of a
// socket under the test's own directory: each end reads the other's
// supplementary groups as this process has them (getgroups(2)), in the order
// the kernel keeps them, sorted.
func TestPeerGroupsAreTheKernels(t *testing.T) {
	t.Parallel()
	want, err := os.Getgroups()
	if err != nil {
		t.Skipf("this process's groups cannot be read: %v", err)
	}
	path := filepath.Join(t.TempDir(), "g.sock")
	if len(path) > maxPath {
		t.Skipf("the test's directory is too deep for a socket path (%d bytes)", len(path))
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeBestEffort(ln, path)
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		c, aerr := ln.AcceptUnix()
		if aerr != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	dialled, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeBestEffort(dialled, path)
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	defer closeBestEffort(server, path)
	slices.Sort(want)
	for side, c := range map[string]*net.UnixConn{"dialler": dialled, "listener": server} {
		got := peerGroups(c)
		if got == nil {
			t.Skip("this kernel does not answer SO_PEERGROUPS (Linux 4.13)")
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("the %s's peer groups = %v, want this process's %v", side, got, want)
		}
	}
}
