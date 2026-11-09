//go:build unix

package rpc_test

import (
	"context"
	"path/filepath"
	"testing"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/ipc"
	"github.com/kitsunium/sdk/internal/service/proc/ipc/rpc"
)

// TestACallCrossesARealPrivateSocket serves a Methods table on a proc/ipc
// listener under the test's own directory and calls it through a proc/ipc
// dialer: the frames ride the Unix socket, and the handler reads the peer the
// kernel named. Where t.TempDir() is longer than sun_path (macOS's
// /var/folders), the listener's refusal is checked and the test skips.
func TestACallCrossesARealPrivateSocket(t *testing.T) {
	t.Parallel()
	cfg := ipc.Config{Path: filepath.Join(t.TempDir(), "run", "rpc.sock")}
	ln, err := ipc.NewListener(&cfg)
	if errs.HasCode(err, coreipc.CodeMisconfigured) {
		t.Skipf("t.TempDir() is past this kernel's sun_path, refused as expected: %v", err)
	}
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	srv, err := rpc.NewServer(serverConfig(4), rpc.Methods{"echo": rpc.HandlerFunc(echo)})
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	d, err := ipc.NewDialer(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := rpc.NewClient(context.Background(), d, clientConfig(4))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	var out map[string]any
	if err := c.Call(context.Background(), "echo", map[string]any{"n": 1}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out["n"] != float64(1) {
		t.Fatalf("Call answered %v", out)
	}
	closeOrLog(t, c)
	closeOrLog(t, srv)
	if err := <-served; err != nil {
		t.Fatalf("Serve after Close: %v", err)
	}
}
