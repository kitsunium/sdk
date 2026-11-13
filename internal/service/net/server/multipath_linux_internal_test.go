//go:build linux

package server

import (
	"context"
	"os"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// goDefaultMultipath reports whether a listener of Go's net.ListenConfig left
// at its default is Multipath TCP here, read back as State reads it: true
// where the kernel has MPTCP and GODEBUG's multipathtcp keeps Go's default.
func goDefaultMultipath(t *testing.T) bool {
	t.Helper()
	ln, rep, err := listen(t.Context(), corenet.AddressValue{Network: "tcp", Addr: "127.0.0.1:0"}, nil, socketSpec{})
	if err != nil {
		t.Fatal(err)
	}
	swallowErr(ln.Close())
	return rep.traits.has(traitMultipath)
}

// startMultipathCase starts one group under vars — a loop group on v, or the
// goroutine engine when v is nil — and returns its one listener row.
func startMultipathCase(t *testing.T, vars map[string]string, v *corenet.LoopValue) corenet.ListenerStateValue {
	t.Helper()
	srv := newServer(envOf(vars))
	t.Cleanup(func() { swallowErr(srv.Close()) })
	if v != nil {
		srv.Group("g", Listen("tcp", "127.0.0.1:0"), Loop(*v)).HandleLoop(&loopLineH{})
	} else {
		srv.Group("g", Listen("tcp", "127.0.0.1:0")).Handle(corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil }))
	}
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
	}
	rows := srv.State().Listeners
	if len(rows) != 1 {
		t.Fatalf("listener rows = %+v, want one", rows)
	}
	return rows[0]
}

// TestMultipathTCP_sendZCGroupListensPlain pins the owner's decision on #969:
// a loop group that opts into uring-send-zc listens with plain TCP — State's
// row reads MultipathTCP false, read back from the socket —, so the kernel
// can send its connections zero-copy, while every other group keeps Go's
// default: a loop group that did not opt in, the goroutine engine, a group
// whose row the operator switched off and one capped at epoll, each reading
// what a listener of Go's left at its default reads here (MPTCP where the
// kernel has it). Where the default is plain TCP already, the test says so
// and the rows can only agree.
func TestMultipathTCP_sendZCGroupListensPlain(t *testing.T) {
	def := goDefaultMultipath(t)
	if !def {
		t.Logf("Go's default listener is plain TCP here (GODEBUG=%q, net.mptcp.enabled off or absent): only the opted-in group's row is told apart", os.Getenv("GODEBUG"))
	}
	zc := corenet.LoopValue{Capabilities: []corenet.Capability{corenet.CapabilityURingSendZC}}
	epollZC := zc
	epollZC.Backend = corenet.LoopBackendEpoll
	for _, tc := range []struct {
		name string
		vars map[string]string
		v    *corenet.LoopValue
		want bool
	}{
		{"send-zc", nil, &zc, false},
		{"loop", nil, &corenet.LoopValue{}, def},
		{"goroutine", nil, nil, def},
		{"send-zc-switched-off", map[string]string{corenet.EnvDisable: string(corenet.CapabilityURingSendZC)}, &zc, def},
		{"send-zc-on-epoll", nil, &epollZC, def},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := startMultipathCase(t, tc.vars, tc.v)
			if row.MultipathTCP != tc.want {
				t.Fatalf("MultipathTCP = %v, want %v (Go's default here: %v)", row.MultipathTCP, tc.want, def)
			}
		})
	}
}
