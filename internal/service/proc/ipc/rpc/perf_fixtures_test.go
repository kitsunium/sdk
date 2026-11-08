//go:build !race

// The fixture of the call's performance contract (design/sdk.yaml, budgets;
// issue #930, ADR 0218): it sets one Call up and returns it, and the
// gen_perf_test.go kit gen writes beside this file counts its allocations —
// the client's and the server's, process-wide — against its budget. Race off,
// since the race detector allocates on its own.
package rpc_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/ipc/rpc"
)

// perfCallRoundTrip is Call's fixture: one small record sent over a
// net.Pipe, decoded, echoed and decoded back — the frame, the codec, the
// server's slot and handler goroutine included, which is what
// BenchmarkCallRoundTrip measures. The client and the server live as long as
// the test.
func perfCallRoundTrip(tb testing.TB) func() {
	srv, err := rpc.NewServer(serverConfig(64), rpc.Methods{"echo": rpc.HandlerFunc(echoValue)})
	if err != nil {
		tb.Fatalf("NewServer() = %v", err)
	}
	d := &pipeDialer{srv: srv, served: make(chan error, 1)}
	c, err := rpc.NewClient(context.Background(), d, clientConfig(64))
	if err != nil {
		tb.Fatalf("NewClient() = %v", err)
	}
	tb.Cleanup(func() {
		if closeErr := c.Close(); closeErr != nil {
			tb.Errorf("Close() = %v", closeErr)
		}
		if closeErr := srv.Close(); closeErr != nil {
			tb.Errorf("Close() = %v", closeErr)
		}
	})
	in := benchValue{ID: 7, Name: "statusline", Ready: true}
	var out benchValue
	ctx := context.Background()
	return func() {
		if callErr := c.Call(ctx, "echo", in, &out); callErr != nil {
			tb.Fatalf("Call() = %v", callErr)
		}
	}
}
