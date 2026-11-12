// Package server_test — MaxBodyBytes, the bound on a request body an HTTP
// group serves through net/http (#845).
package server_test

import (
	"bufio"
	"fmt"
	"io"
	stdnet "net"
	"net/http"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestMaxBodyBytes_minusOneStatesNoBound pins that -1 lets the same 2 MiB
// body through, and that an explicit bound is the bound.
func TestMaxBodyBytes_minusOneStatesNoBound(t *testing.T) {
	t.Parallel()
	open := startReadAllServer(t, server.MaxBodyBytes(-1)).State().Listeners[0].Address
	if status, answer := postStatus(t, open, 2<<20, 2<<20); status != http.StatusOK || !strings.Contains(answer, bodyAnswer+" 2097152") {
		t.Errorf("a 2 MiB POST under MaxBodyBytes(-1) answered %d %q, want 200 and the whole body", status, answer)
	}
	small := startReadAllServer(t, server.MaxBodyBytes(1<<10)).State().Listeners[0].Address
	if status, _ := postStatus(t, small, 4<<10, 1<<10); status != http.StatusRequestEntityTooLarge {
		t.Errorf("a 4 KiB POST under MaxBodyBytes(1 KiB) answered %d, want 413", status)
	}
	if status, _ := postStatus(t, small, 1<<10, 1<<10); status != http.StatusOK {
		t.Errorf("a 1 KiB POST under MaxBodyBytes(1 KiB) answered %d, want 200: the bound is inclusive", status)
	}
}

// TestMaxBodyBytes_chunkedBodyEndsInTheStdlibError pins the second half: a body
// of no declared length is bounded as it is read, and the handler's read ends
// in net/http's *http.MaxBytesError, which a handler maps to 413 as this one does.
func TestMaxBodyBytes_chunkedBodyEndsInTheStdlibError(t *testing.T) {
	t.Parallel()
	addr := startReadAllServer(t, server.MaxBodyBytes(1<<10)).State().Listeners[0].Address
	conn, err := stdnet.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, conn) })
	if deadlineErr := conn.SetDeadline(time.Now().Add(boundsDeadline)); deadlineErr != nil {
		t.Fatalf("deadline: %v", deadlineErr)
	}
	chunk := strings.Repeat("a", 512)
	request := "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n" +
		strings.Repeat(fmt.Sprintf("%x\r\n%s\r\n", len(chunk), chunk), 8) + "0\r\n\r\n"
	if _, writeErr := io.WriteString(conn, request); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(line, "413") {
		t.Fatalf("a chunked 4 KiB body under a 1 KiB bound answered %q, %v; want 413 from the handler", line, err)
	}
}

// TestMaxBodyBytes_stateReportsTheBound pins that State says what bound an HTTP
// listener holds: the default's 1 MiB, an explicit value, and none as -1.
func TestMaxBodyBytes_stateReportsTheBound(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		opts []server.GroupOption
		want int64
	}{
		{"default", nil, 1 << 20},
		{"explicit", []server.GroupOption{server.MaxBodyBytes(4 << 10)}, 4 << 10},
		{"none", []server.GroupOption{server.MaxBodyBytes(-1)}, -1},
	} {
		got := startReadAllServer(t, c.opts...).State().Listeners[0].MaxBodyBytes
		if got != c.want {
			t.Errorf("%s: ListenerStateValue.MaxBodyBytes = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestMaxBodyBytes_invalidValueIsRefused pins that a value below -1 is neither
// a bound nor "none": Start refuses it, naming the option.
func TestMaxBodyBytes_invalidValueIsRefused(t *testing.T) {
	t.Parallel()
	srv := server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.Plaintext(), server.MaxBodyBytes(-2)).HandleHTTP(http.NotFoundHandler())
	err := srv.Start(t.Context())
	t.Cleanup(func() { _ = srv.Close() })
	if !errs.HasCode(err, corenet.CodeOptionValueInvalid) || !fieldIs(err, "option", "MaxBodyBytes") {
		t.Fatalf("Start = %v, want OptionValueInvalid naming MaxBodyBytes", err)
	}
}

// TestMaxBodyBytes_refusedWhereNoHTTPAdapterRuns pins ADR 0187's rule for the
// option: a loop group and a datagram group refuse it at Start, naming it —
// the loop's body bound is the http1 profile's own (HTTP1LimitsValue.Body).
func TestMaxBodyBytes_refusedWhereNoHTTPAdapterRuns(t *testing.T) {
	t.Parallel()
	t.Run("loop", func(t *testing.T) {
		t.Parallel()
		srv := server.New()
		codec := http1.HandleLoop(corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
			res.Body = []byte("ok")
		}), http1.Parser{})
		srv.Group("l", server.Listen("tcp", "127.0.0.1:0"), server.Plaintext(), server.Loop(corenet.LoopValue{Loops: 1}), server.MaxBodyBytes(1<<20)).HandleLoop(codec)
		err := srv.Start(t.Context())
		t.Cleanup(func() { _ = srv.Close() })
		if !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) || !fieldIs(err, "option", "MaxBodyBytes") {
			t.Fatalf("Start = %v, want LoopOptionUnsupported naming MaxBodyBytes", err)
		}
	})
	t.Run("datagram", func(t *testing.T) {
		t.Parallel()
		srv := server.New()
		srv.PacketGroup("p", server.Listen("udp", "127.0.0.1:0"), server.MaxBodyBytes(1<<20))
		err := srv.Start(t.Context())
		t.Cleanup(func() { _ = srv.Close() })
		if !errs.HasCode(err, corenet.CodePacketOptionUnsupported) || !fieldIs(err, "option", "MaxBodyBytes") {
			t.Fatalf("Start = %v, want PacketOptionUnsupported naming MaxBodyBytes", err)
		}
	})
}
