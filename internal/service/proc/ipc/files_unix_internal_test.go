//go:build unix

package ipc

import (
	"context"
	"path/filepath"
	"testing"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
)

// filePair connects a dialer to a listener on a private socket under the
// test's own directory and returns both ends, closed when the test ends.
func filePair(t *testing.T) (client, server *coreipc.Conn) {
	t.Helper()
	cfg := Config{Path: filepath.Join(t.TempDir(), "run", "files.sock")}
	ln, err := NewListener(&cfg)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan *coreipc.Conn, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	client, err = Dial(context.Background(), &cfg)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server = <-accepted
	if server == nil {
		t.Fatal("Accept failed")
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}
