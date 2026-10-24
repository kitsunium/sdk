// Package server_test — the Loop group option, refused by every group until a
// loop handler can serve one.
package server_test

import (
	"net/http"
	"slices"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestLoop pins that Loop is judged at Start, once the handler is known, and
// refused by every group no loop handler serves (ADR 0186 §D1, step 8): before
// or after the handler's declaration, a zero value included, on a stream or a
// datagram group. HandlerMissing and HandlerAlreadySet win over the refusal,
// because they explain it. Nothing is bound by a refused Start.
func TestLoop(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// declare builds the server under test.
		declare func(srv *server.Server)
		// want is the code Start must fail with.
		want errs.Code
	}
	loop := server.Loop(corenet.LoopValue{Backend: corenet.LoopBackendEpoll, Loops: 1})
	tests := []tc{
		{
			name: "before Handle",
			declare: func(srv *server.Server) {
				srv.Group("g", loop, server.Listen("tcp", "127.0.0.1:0")).Handle(echoConnHandler{})
			},
			want: corenet.CodeLoopOptionUnsupported,
		},
		{
			name: "after HandleHTTP",
			declare: func(srv *server.Server) {
				srv.Group("g", server.Listen("tcp", "127.0.0.1:0")).HandleHTTP(http.NotFoundHandler())
				srv.Group("h", server.Listen("tcp", "127.0.0.1:0"), server.Loop(corenet.LoopValue{})).HandleFunc(echoHandler)
			},
			want: corenet.CodeLoopOptionUnsupported,
		},
		{
			name: "on a datagram group",
			declare: func(srv *server.Server) {
				srv.PacketGroup("p", loop, server.Listen("udp", "127.0.0.1:0"))
			},
			want: corenet.CodeLoopOptionUnsupported,
		},
		{
			name: "with no handler, HandlerMissing wins",
			declare: func(srv *server.Server) {
				srv.Group("g", loop, server.Listen("tcp", "127.0.0.1:0"))
			},
			want: corenet.CodeHandlerMissing,
		},
		{
			name: "with two handlers, HandlerAlreadySet wins",
			declare: func(srv *server.Server) {
				srv.Group("g", loop, server.Listen("tcp", "127.0.0.1:0")).
					Handle(echoConnHandler{}).HandleFunc(echoHandler)
			},
			want: corenet.CodeHandlerAlreadySet,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		c.declare(srv)
		err := srv.Start(t.Context())
		if !errs.HasCode(err, c.want) {
			t.Fatalf("Start = %v, want code %v", err, c.want)
		}
		if c.want == corenet.CodeLoopOptionUnsupported && !slices.ContainsFunc(errs.FieldsOf(err), func(f errs.FieldValue) bool {
			return f.Key() == "option" && f.StringValue() == "Loop"
		}) {
			t.Fatalf("fields = %v, want option=Loop", errs.FieldsOf(err))
		}
		if st := srv.State(); len(st.Listeners) != 0 || len(st.Loops) != 0 {
			t.Fatalf("a refused Start left %d listeners and %d loop groups", len(st.Listeners), len(st.Loops))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
