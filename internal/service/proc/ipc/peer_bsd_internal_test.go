//go:build freebsd || dragonfly || netbsd

package ipc

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// TestPeerOfOnTheBSDs pins #1093 on FreeBSD, DragonFly and NetBSD: both ends
// of a socket under the test's own directory are named by the kernel as this
// account — the dialler's peer is the listener and the accepted side's peer
// the dialler. Since #1092 the xucred's groups are read too (the effective
// gid among them); NetBSD's unpcbid carries none.
func TestPeerOfOnTheBSDs(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "peer.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
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
		t.Fatalf("dial: %v", err)
	}
	defer dialled.Close()
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	defer server.Close()
	for side, c := range map[string]*net.UnixConn{"dialler": dialled, "listener": server} {
		peer := peerOf(c)
		if !peer.Verified || peer.UID != os.Geteuid() || peer.GID != os.Getegid() {
			t.Errorf("%s's peer = %+v, want this account (%d:%d), verified", side, peer, os.Geteuid(), os.Getegid())
		}
		groups := peerGroups(c)
		switch runtime.GOOS {
		case "netbsd":
			if groups != nil {
				t.Errorf("%s's peer groups = %v, want none from unpcbid", side, groups)
			}
		default:
			if !slices.Contains(groups, os.Getegid()) {
				t.Errorf("%s's peer groups = %v, want the effective gid %d among them", side, groups, os.Getegid())
			}
		}
	}
}
