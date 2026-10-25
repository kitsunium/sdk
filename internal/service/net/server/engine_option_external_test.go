// Package server_test — Plaintext, Name and Engine (ADR 0187): declared,
// honoured, and refused at Start whenever they contradict the group's handler
// or another option, never replaced by a fallback.
package server_test

import (
	"net/http"
	"slices"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestEngine pins what Start does with Engine and Plaintext on a group that is
// not served by a loop handler (the loop group's cases are the internal
// TestLoopGroup_refusals): the engine the handler's type has is served, every
// other one is refused with EngineUnsupported, and Plaintext with TLS, in
// either order, with PlaintextWithTLS. HandlerMissing still wins, because it
// explains the rest. Nothing is bound by a refused Start.
func TestEngine(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// declare builds the server under test.
		declare func(srv *server.Server)
		// want is the code Start must fail with; zero means served.
		want errs.Code
		// wantEngine is the "engine" field the refusal must carry, if any.
		wantEngine string
	}
	listen := server.Listen("tcp", "127.0.0.1:0")
	udp := server.Listen("udp", "127.0.0.1:0")
	tlsZero := server.TLS(corenet.IdentityValue{})
	tests := []tc{
		{
			name: "EngineGoroutine on Handle is served",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, server.Engine(corenet.EngineGoroutine)).Handle(echoConnHandler{})
			},
		},
		{
			name: "EngineAuto on HandleHTTP is served",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, server.Engine(corenet.EngineAuto)).HandleHTTP(http.NotFoundHandler())
			},
		},
		{
			name: "Plaintext on HandleFunc is served",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, server.Plaintext()).HandleFunc(echoHandler)
			},
		},
		{
			name: "EngineLoop on Handle",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, server.Engine(corenet.EngineLoop)).Handle(echoConnHandler{})
			},
			want:       corenet.CodeEngineUnsupported,
			wantEngine: "loop",
		},
		{
			name: "EngineLoop on HandleHTTP",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, server.Engine(corenet.EngineLoop)).HandleHTTP(http.NotFoundHandler())
			},
			want:       corenet.CodeEngineUnsupported,
			wantEngine: "loop",
		},
		{
			name: "EngineGoroutine with Loop",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, server.Engine(corenet.EngineGoroutine), server.Loop(corenet.LoopValue{})).
					HandleFunc(echoHandler)
			},
			want:       corenet.CodeEngineUnsupported,
			wantEngine: "goroutine",
		},
		{
			name: "an engine outside the block",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, server.Engine(corenet.EngineLoop+1)).HandleFunc(echoHandler)
			},
			want:       corenet.CodeEngineUnsupported,
			wantEngine: "unknown",
		},
		{
			name: "Plaintext then TLS",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, server.Plaintext(), tlsZero).HandleFunc(echoHandler)
			},
			want: corenet.CodePlaintextWithTLS,
		},
		{
			name: "TLS then Plaintext",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, tlsZero, server.Plaintext()).HandleHTTP(http.NotFoundHandler())
			},
			want: corenet.CodePlaintextWithTLS,
		},
		{
			name: "with no handler, HandlerMissing wins",
			declare: func(srv *server.Server) {
				srv.Group("g", listen, server.Engine(corenet.EngineLoop), server.Plaintext(), tlsZero)
			},
			want: corenet.CodeHandlerMissing,
		},
		{
			name: "EngineGoroutine on a datagram group is served",
			declare: func(srv *server.Server) {
				srv.PacketGroup("p", udp, server.Engine(corenet.EngineGoroutine), server.Plaintext()).HandleFunc(noopPacket)
			},
		},
		{
			name: "EngineLoop on a datagram group",
			declare: func(srv *server.Server) {
				srv.PacketGroup("p", udp, server.Engine(corenet.EngineLoop)).HandleFunc(noopPacket)
			},
			want:       corenet.CodeEngineUnsupported,
			wantEngine: "loop",
		},
		{
			name: "Plaintext with TLS on a datagram group",
			declare: func(srv *server.Server) {
				srv.PacketGroup("p", udp, server.Plaintext(), tlsZero).HandleFunc(noopPacket)
			},
			want: corenet.CodePlaintextWithTLS,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		c.declare(srv)
		err := srv.Start(t.Context())
		if c.want == 0 {
			if err != nil {
				t.Fatalf("Start = %v, want served", err)
			}
			return
		}
		if !errs.HasCode(err, c.want) {
			t.Fatalf("Start = %v, want code %v", err, c.want)
		}
		if c.wantEngine != "" && !slices.ContainsFunc(errs.FieldsOf(err), func(f errs.FieldValue) bool {
			return f.Key() == "engine" && f.StringValue() == c.wantEngine
		}) {
			t.Fatalf("fields %v, want engine=%s", errs.FieldsOf(err), c.wantEngine)
		}
		if n := len(srv.State().Listeners); n != 0 {
			t.Fatalf("a refused Start bound %d listeners", n)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestName pins Name (ADR 0187): it names a group declared with an empty name,
// repeats one declared with a name, and fails Start with GroupNameConflict
// when it is empty or differs; the name it sets is unique like any other.
func TestName(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// declare builds the server under test.
		declare func(srv *server.Server)
		// want is the code Start must fail with; zero means served.
		want errs.Code
		// wantGroup is the group name State must report once served.
		wantGroup string
	}
	listen := server.Listen("tcp", "127.0.0.1:0")
	tests := []tc{
		{
			name: "it names a group declared without one",
			declare: func(srv *server.Server) {
				srv.Group("", listen, server.Name("api")).HandleFunc(echoHandler)
			},
			wantGroup: "api",
		},
		{
			name: "it may repeat the declared name",
			declare: func(srv *server.Server) {
				srv.Group("api", listen, server.Name("api")).HandleFunc(echoHandler)
			},
			wantGroup: "api",
		},
		{
			name: "it names a datagram group too",
			declare: func(srv *server.Server) {
				srv.PacketGroup("", server.Listen("udp", "127.0.0.1:0"), server.Name("dns")).HandleFunc(noopPacket)
			},
			wantGroup: "dns",
		},
		{
			name: "a different name",
			declare: func(srv *server.Server) {
				srv.Group("api", listen, server.Name("web")).HandleFunc(echoHandler)
			},
			want: corenet.CodeGroupNameConflict,
		},
		{
			name: "an empty Name",
			declare: func(srv *server.Server) {
				srv.Group("api", listen, server.Name("")).HandleFunc(echoHandler)
			},
			want: corenet.CodeGroupNameConflict,
		},
		{
			name: "a different name on a datagram group",
			declare: func(srv *server.Server) {
				srv.PacketGroup("dns", server.Listen("udp", "127.0.0.1:0"), server.Name("ntp")).HandleFunc(noopPacket)
			},
			want: corenet.CodeGroupNameConflict,
		},
		{
			name: "the name it sets is unique",
			declare: func(srv *server.Server) {
				srv.Group("api", listen).HandleFunc(echoHandler)
				srv.Group("", listen, server.Name("api")).HandleFunc(echoHandler)
			},
			want: corenet.CodeGroupDuplicate,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		c.declare(srv)
		err := srv.Start(t.Context())
		if c.want != 0 {
			if !errs.HasCode(err, c.want) {
				t.Fatalf("Start = %v, want code %v", err, c.want)
			}
			return
		}
		if err != nil {
			t.Fatalf("Start = %v, want served", err)
		}
		listeners := srv.State().Listeners
		if len(listeners) != 1 || listeners[0].Group != c.wantGroup {
			t.Fatalf("State().Listeners = %+v, want one of group %q", listeners, c.wantGroup)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
