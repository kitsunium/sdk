// Package server — the kqueue backend as server chooses it (ADR 0222): it
// starts on darwin and the BSDs, the operator's switches lower it, and every
// other platform refuses it by the shared sentinel.
package server

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// startKqueueGroup starts a loop group asking for backend under vars and
// returns the server, or the Start error.
func startKqueueGroup(t *testing.T, vars map[string]string, backend corenet.LoopBackend) (*Server, error) {
	t.Helper()
	srv := newServer(envOf(vars))
	t.Cleanup(func() { swallowErr(srv.Close()) })
	srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: backend})).HandleLoop(&loopLineH{})
	return srv, srv.Start(t.Context())
}

// TestLoopKqueue_refusedOffItsPlatforms asks for kqueue where the platform's
// poller is not kqueue — Linux, Windows, illumos — and requires
// UnsupportedPlatform, as epoll and io_uring are refused off Linux; on
// darwin and the BSDs it asks for epoll and io_uring and requires the same.
func TestLoopKqueue_refusedOffItsPlatforms(t *testing.T) {
	t.Parallel()
	refused := []corenet.LoopBackend{corenet.LoopBackendKqueue}
	if nativePoller == corenet.CapabilityKqueue {
		refused = []corenet.LoopBackend{corenet.LoopBackendIOURing, corenet.LoopBackendEpoll}
	}
	for _, b := range refused {
		t.Run(b.String(), func(t *testing.T) {
			t.Parallel()
			_, err := startKqueueGroup(t, nil, b)
			if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
				t.Fatalf("Start with %v = %v, want UnsupportedPlatform", b, err)
			}
		})
	}
}

// TestLoopKqueue_autoAndTheOperator starts auto and an explicit kqueue on
// darwin and the BSDs: auto runs portable, not degraded — kqueue is opt-in
// until its paired gate holds (ADR 0222 §D6) —; the explicit kqueue runs on
// it, its probe's last stage named, not degraded; KITSUNIUM_NET_ENGINE=portable
// lowers it, the override naming the cap, and an explicit kqueue the operator
// switched off runs portable, as an explicit epoll does on Linux. The suite runs on the darwin lane and
// the BSD guests (ADR 0222); elsewhere the refusal above is the platform's
// contract.
func TestLoopKqueue_autoAndTheOperator(t *testing.T) {
	t.Parallel()
	if nativePoller != corenet.CapabilityKqueue {
		t.Skip("kqueue is darwin's and the BSDs': TestLoopKqueue_refusedOffItsPlatforms holds the refusal here")
	}
	cases := []struct {
		name    string
		vars    map[string]string
		backend corenet.LoopBackend
		want    corenet.LoopBackend
		applied corenet.Capability
	}{
		{"auto stays portable", nil, corenet.LoopBackendAuto, corenet.LoopBackendPortable, ""},
		{"explicit", nil, corenet.LoopBackendKqueue, corenet.LoopBackendKqueue, ""},
		{"capped at portable", map[string]string{corenet.EnvEngine: "portable"}, corenet.LoopBackendKqueue, corenet.LoopBackendPortable, corenet.CapabilityPortable},
		{"switched off", map[string]string{corenet.EnvDisable: "kqueue"}, corenet.LoopBackendKqueue, corenet.LoopBackendPortable, corenet.CapabilityIOURing},
		{"epoll switched off leaves kqueue", map[string]string{corenet.EnvDisable: "epoll"}, corenet.LoopBackendKqueue, corenet.LoopBackendKqueue, corenet.CapabilityIOURing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, err := startKqueueGroup(t, tc.vars, tc.backend)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			st := srv.State()
			ls := st.Loops[0]
			if ls.Backend != tc.want || ls.Degraded {
				t.Fatalf("backend %v degraded %v (%q), want %v", ls.Backend, ls.Degraded, ls.DegradedReason, tc.want)
			}
			if tc.want == corenet.LoopBackendKqueue && ls.ProbeStage != "kqueue-socket" {
				t.Fatalf("probe stage %q, want kqueue-socket", ls.ProbeStage)
			}
			if st.Override.Applied != tc.applied {
				t.Fatalf("override %+v, want applied %q", st.Override, tc.applied)
			}
			if c := loopDial(t, st.Listeners[0].Address); c == nil {
				t.Fatal("no connection")
			}
		})
	}
}
