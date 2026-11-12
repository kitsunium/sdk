// Package server_test — the Backlog group option (#978).
package server_test

import (
	stdnet "net"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestBacklog_stateReportsTheDepthApplied pins ListenerStateValue.Backlog: the
// option's value where the host allows it, and the host's somaxconn for the
// default. Windows keeps the platform's own and says so.
func TestBacklog_stateReportsTheDepthApplied(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		srv := server.New()
		srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.Plaintext(), server.Backlog(8)).HandleHTTP(http.NotFoundHandler())
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { closeOrFail(t, srv) })
		if row := srv.State().Listeners[0]; !row.Degraded || row.DegradedReason == "" {
			t.Fatalf("Windows keeps its own backlog and must say so: %+v", row)
		}
		return
	}
	serve := func(opts ...server.GroupOption) corenet.ListenerStateValue {
		t.Helper()
		srv := server.New()
		srv.Group("api", append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0"), server.Plaintext()}, opts...)...).HandleHTTP(http.NotFoundHandler())
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { closeOrFail(t, srv) })
		return srv.State().Listeners[0]
	}
	if row := serve(server.Backlog(1)); row.Backlog != 1 || row.Degraded {
		t.Errorf("Backlog(1): row = %+v, want Backlog 1 and not degraded", row)
	}
	def := serve()
	if def.Backlog < 0 {
		t.Errorf("the default's Backlog = %d", def.Backlog)
	}
	if row := serve(server.Backlog(8)); row.Backlog != 8 && !row.Degraded {
		t.Errorf("Backlog(8): row = %+v, want 8, or the clamp reported", row)
	}
}

// TestBacklog_refusals pins that a negative depth is refused at Start naming
// the option, and a datagram group refuses the option.
func TestBacklog_refusals(t *testing.T) {
	t.Parallel()
	t.Run("negative", func(t *testing.T) {
		t.Parallel()
		srv := server.New()
		srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.Plaintext(), server.Backlog(-1)).HandleHTTP(http.NotFoundHandler())
		err := srv.Start(t.Context())
		t.Cleanup(func() { _ = srv.Close() })
		if !errs.HasCode(err, corenet.CodeOptionValueInvalid) || !fieldIs(err, "option", "Backlog") {
			t.Fatalf("Start = %v, want OptionValueInvalid naming Backlog", err)
		}
	})
	t.Run("datagram", func(t *testing.T) {
		t.Parallel()
		srv := server.New()
		srv.PacketGroup("p", server.Listen("udp", "127.0.0.1:0"), server.Backlog(8))
		err := srv.Start(t.Context())
		t.Cleanup(func() { _ = srv.Close() })
		if !errs.HasCode(err, corenet.CodePacketOptionUnsupported) || !fieldIs(err, "option", "Backlog") {
			t.Fatalf("Start = %v, want PacketOptionUnsupported naming Backlog", err)
		}
	})
}

// TestBacklog_loopGroupServesThroughASmallQueue pins that the loop engine
// honours the option and loses no peer to it: with a backlog of 8, a burst of
// dialers larger than the queue is served whole, the connects the kernel
// dropped retried by their own SYN timers.
func TestBacklog_loopGroupServesThroughASmallQueue(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the loop engine's native backends are Linux's")
	}
	srv := server.New()
	codec := http1.HandleLoop(corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		res.Body = []byte("ok")
	}), http1.Parser{})
	srv.Group("loop", server.Listen("tcp", "127.0.0.1:0"), server.Plaintext(), server.Backlog(8),
		server.Loop(corenet.LoopValue{Backend: corenet.LoopBackendEpoll, Loops: 1})).HandleLoop(codec)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	row := srv.State().Listeners[0]
	if row.Backlog != 8 && !row.Degraded {
		t.Fatalf("row = %+v, want Backlog 8, or the clamp reported", row)
	}
	const dialers = 32
	var wg sync.WaitGroup
	failed := make(chan error, dialers)
	for range dialers {
		wg.Go(func() {
			c, err := stdnet.DialTimeout("tcp", row.Address, 10*time.Second)
			if err != nil {
				failed <- err
				return
			}
			_ = c.Close()
		})
	}
	wg.Wait()
	close(failed)
	for err := range failed {
		t.Errorf("a dialer was lost: %v", err)
	}
}
