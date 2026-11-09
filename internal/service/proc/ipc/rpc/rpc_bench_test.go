package rpc_test

import (
	"context"
	"io"
	"net"
	"testing"

	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
	"github.com/kitsunium/sdk/internal/service/proc/ipc/rpc"
)

// benchValue is the value a benchmark sends and gets back: a small record,
// what a status or control call carries.
type benchValue struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
}

// echoValue answers with the value it decoded.
func echoValue(_ context.Context, req corerpc.Request) (any, error) {
	var v benchValue
	if err := req.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// BenchmarkCallRoundTrip is one Call over a net.Pipe: the request framed and
// encoded, the server's reader, slot and handler goroutine, the reply framed
// and decoded — what the protocol costs above the bytes.
func BenchmarkCallRoundTrip(b *testing.B) {
	srv, err := rpc.NewServer(serverConfig(64), rpc.Methods{"echo": rpc.HandlerFunc(echoValue)})
	if err != nil {
		b.Fatal(err)
	}
	defer srv.Close()
	d := &pipeDialer{srv: srv, served: make(chan error, 1)}
	c, err := rpc.NewClient(context.Background(), d, clientConfig(64))
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	in := benchValue{ID: 7, Name: "statusline", Ready: true}
	var out benchValue
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := c.Call(ctx, "echo", in, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRawWriteRoundTrip is the floor: the same bytes a Call's request
// and reply put on the pipe, written and echoed back with no frame, no codec
// and no goroutine per call.
func BenchmarkRawWriteRoundTrip(b *testing.B) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := io.ReadFull(server, buf); err != nil {
				return
			}
			if _, err := server.Write(buf); err != nil {
				return
			}
		}
	}()
	defer server.Close()
	msg := make([]byte, 64)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := client.Write(msg); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(client, msg); err != nil {
			b.Fatal(err)
		}
	}
}
