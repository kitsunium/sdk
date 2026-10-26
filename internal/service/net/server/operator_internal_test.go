// Package server — the operator's switches, KITSUNIUM_NET_ENGINE and
// KITSUNIUM_NET_DISABLE (ADR 0188): the registry held to the design, R5 on
// every combination, and a loop group served on the backend they leave.
package server

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// designFile is the net domain's design, which declares the registry.
var designFile = filepath.Join("..", "..", "..", "..", "design", "net.yaml")

// designCapability matches one constant of the Capability blocks in the
// design: its blockDoc, its name and its token.
var designCapability = regexp.MustCompile(`(?m)^      - blockDoc: (.*)\n        doc: \|-\n(?:          .*\n)+        name: (\w+)\n        type: Capability\n        value: "\\"([a-z_-]+)\\""$`)

// envOf is a lookup over a map, the injected environment.
func envOf(vars map[string]string) lookupFunc {
	return func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}
}

// TestCapabilityRegistry_matchesTheDesign holds capabilityRegistry to
// design/net.yaml: the same tokens, in the same order, top first, and the
// switchable ones exactly the block KITSUNIUM_NET_DISABLE names. A capability
// added to the design without its row, or a row the design does not declare,
// fails here, and so does a token the registry spells differently.
func TestCapabilityRegistry_matchesTheDesign(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(designFile)
	if err != nil {
		t.Fatalf("reading the design: %v", err)
	}
	found := designCapability.FindAllStringSubmatch(string(raw), -1)
	if len(found) == 0 {
		t.Fatal("no Capability constant found in design/net.yaml: the pattern drifted from the design's layout")
	}
	if len(found) != len(capabilityRegistry) {
		t.Fatalf("design declares %d capabilities, the registry holds %d", len(found), len(capabilityRegistry))
	}
	ranks := make([]int, 0, len(found))
	for i, m := range found {
		blockDoc, token := m[1], corenet.Capability(m[3])
		row := capabilityRegistry[i]
		if row.token != token {
			t.Errorf("registry[%d] = %q, design declares %s = %q", i, row.token, m[2], token)
		}
		switchable := strings.Contains(blockDoc, corenet.EnvDisable)
		if row.switchable != switchable {
			t.Errorf("%q: switchable = %v, the design's block %q says %v", token, row.switchable, blockDoc, switchable)
		}
		kernel := strings.Contains(blockDoc, "capabilities")
		if row.kernel != kernel || kernel && row.rank != rankKernel {
			t.Errorf("%q: kernel = %v rank %d, the design's block %q says kernel %v", token, row.kernel, row.rank, blockDoc, kernel)
		}
		ranks = append(ranks, row.rank)
	}
	if !slices.IsSortedFunc(ranks, func(a, b int) int { return b - a }) {
		t.Errorf("registry ranks %v are not top first", ranks)
	}
	// Each constant the code declares is a row: the pins of gen_api_test.go
	// hold the constants to the design, this holds the rows to the constants.
	for _, c := range []corenet.Capability{
		corenet.CapabilityIOURing, corenet.CapabilityEpoll,
		corenet.CapabilityPortable, corenet.CapabilityGoroutine,
	} {
		if _, ok := lookupCapability(string(c)); !ok {
			t.Errorf("%q has no row in the registry", c)
		}
	}
}

// TestReadOperator pins how the two variables are read: an empty value is
// unset, a token is matched exactly, an unknown one — or a floor named in
// KITSUNIUM_NET_DISABLE — caps the engines at portable and is a fault, and the
// override reports the source, what was read and what was applied.
func TestReadOperator(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		vars     map[string]string
		source   string
		raw      string
		applied  corenet.Capability
		disabled []corenet.Capability
		faults   int
	}{
		{name: "unset", vars: nil},
		{name: "empty values are unset", vars: map[string]string{corenet.EnvEngine: "", corenet.EnvDisable: "  "}},
		{
			name: "engine iouring", vars: map[string]string{corenet.EnvEngine: "iouring"},
			source: "environment", raw: "KITSUNIUM_NET_ENGINE=iouring", applied: corenet.CapabilityIOURing,
		},
		{
			name: "engine epoll", vars: map[string]string{corenet.EnvEngine: " epoll "},
			source: "environment", raw: "KITSUNIUM_NET_ENGINE=epoll", applied: corenet.CapabilityEpoll,
		},
		{
			name: "engine portable", vars: map[string]string{corenet.EnvEngine: "portable"},
			source: "environment", raw: "KITSUNIUM_NET_ENGINE=portable", applied: corenet.CapabilityPortable,
		},
		{
			name: "engine goroutine", vars: map[string]string{corenet.EnvEngine: "goroutine"},
			source: "environment", raw: "KITSUNIUM_NET_ENGINE=goroutine", applied: corenet.CapabilityGoroutine,
		},
		{
			name: "engine unknown caps at portable", vars: map[string]string{corenet.EnvEngine: "kqueue"},
			source: "environment", raw: "KITSUNIUM_NET_ENGINE=kqueue", applied: corenet.CapabilityPortable, faults: 1,
		},
		{
			name: "engine is case-sensitive", vars: map[string]string{corenet.EnvEngine: "EPOLL"},
			source: "environment", raw: "KITSUNIUM_NET_ENGINE=EPOLL", applied: corenet.CapabilityPortable, faults: 1,
		},
		{
			name: "disable iouring", vars: map[string]string{corenet.EnvDisable: "iouring"},
			source: "environment", raw: "KITSUNIUM_NET_DISABLE=iouring", applied: corenet.CapabilityEpoll,
			disabled: []corenet.Capability{corenet.CapabilityIOURing},
		},
		{
			name: "disable both, items trimmed, repeats once", vars: map[string]string{corenet.EnvDisable: " epoll,, iouring ,epoll"},
			source: "environment", raw: "KITSUNIUM_NET_DISABLE=epoll,, iouring ,epoll", applied: corenet.CapabilityPortable,
			disabled: []corenet.Capability{corenet.CapabilityEpoll, corenet.CapabilityIOURing},
		},
		{
			name: "disable epoll alone keeps iouring on top", vars: map[string]string{corenet.EnvDisable: "epoll"},
			source: "environment", raw: "KITSUNIUM_NET_DISABLE=epoll", applied: corenet.CapabilityIOURing,
			disabled: []corenet.Capability{corenet.CapabilityEpoll},
		},
		{
			name: "disable a floor is unknown", vars: map[string]string{corenet.EnvDisable: "portable"},
			source: "environment", raw: "KITSUNIUM_NET_DISABLE=portable", applied: corenet.CapabilityPortable, faults: 1,
		},
		{
			name: "disable unknown beside a known one", vars: map[string]string{corenet.EnvDisable: "iouring,zcrx"},
			source: "environment", raw: "KITSUNIUM_NET_DISABLE=iouring,zcrx", applied: corenet.CapabilityPortable,
			disabled: []corenet.Capability{corenet.CapabilityIOURing}, faults: 1,
		},
		{
			name: "both, in order", vars: map[string]string{corenet.EnvEngine: "epoll", corenet.EnvDisable: "epoll"},
			source: "environment", raw: "KITSUNIUM_NET_ENGINE=epoll KITSUNIUM_NET_DISABLE=epoll", applied: corenet.CapabilityPortable,
			disabled: []corenet.Capability{corenet.CapabilityEpoll},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := readOperator(envOf(tc.vars))
			o := env.override
			if o.Source != tc.source || o.Raw != tc.raw || o.Applied != tc.applied || !slices.Equal(o.Disabled, tc.disabled) {
				t.Fatalf("override = %+v, want source %q raw %q applied %q disabled %v", o, tc.source, tc.raw, tc.applied, tc.disabled)
			}
			if len(env.faults) != tc.faults {
				t.Fatalf("faults = %+v, want %d", env.faults, tc.faults)
			}
			for _, f := range env.faults {
				if f.Source != "environment" || !strings.Contains(f.Reason, "capped at portable") {
					t.Fatalf("fault = %+v", f)
				}
			}
		})
	}
}

// rankOf is the lattice rank of a loop backend; auto is io_uring, its top.
func rankOf(b corenet.LoopBackend) int {
	switch b {
	case corenet.LoopBackendEpoll:
		return rankEpoll
	case corenet.LoopBackendPortable:
		return rankPortable
	default:
		return rankIOURing
	}
}

// TestOperatorPlan pins R5 for a loop group: the plan is never above the
// code's choice nor the environment's ceiling, never a backend the
// environment switched off, the highest that is left, and keeps the fallback
// to portable only when the code asked for auto. Off Linux it is portable.
func TestOperatorPlan(t *testing.T) {
	t.Parallel()
	codes := []corenet.LoopBackend{corenet.LoopBackendAuto, corenet.LoopBackendIOURing, corenet.LoopBackendEpoll, corenet.LoopBackendPortable}
	engines := []string{"", "iouring", "epoll", "portable", "goroutine", "bogus"}
	disables := []string{"", "iouring", "epoll", "iouring,epoll", "bogus"}
	for _, code := range codes {
		for _, engine := range engines {
			for _, disable := range disables {
				env := readOperator(envOf(map[string]string{corenet.EnvEngine: engine, corenet.EnvDisable: disable}))
				name := code.String() + "/" + engine + "/" + disable
				p := env.plan(code, true)
				got := rankOf(p.backend)
				if p.backend == corenet.LoopBackendAuto && !(env.allows(rankIOURing) && env.allows(rankEpoll)) {
					t.Errorf("%s: auto kept although the environment rules a native backend out", name)
				}
				if got > rankOf(code) {
					t.Errorf("%s: plan %v above the code's choice", name, p.backend)
				}
				if p.backend != corenet.LoopBackendPortable && !env.allows(got) {
					t.Errorf("%s: plan %v is a backend the environment rules out", name, p.backend)
				}
				// The highest backend left: nothing allowed between the plan and the code.
				for r := got + 1; r <= rankOf(code); r++ {
					if r > rankPortable && env.allows(r) {
						t.Errorf("%s: plan %v skips an allowed rank %d", name, p.backend, r)
					}
				}
				if p.fallback != (code == corenet.LoopBackendAuto && p.backend != corenet.LoopBackendPortable) {
					t.Errorf("%s: fallback = %v", name, p.fallback)
				}
				if off := env.plan(code, false); code != corenet.LoopBackendIOURing && code != corenet.LoopBackendEpoll &&
					(off.backend != corenet.LoopBackendPortable || off.fallback) {
					t.Errorf("%s off Linux: %+v", name, off)
				}
			}
		}
	}
	// The cases an operator reaches for, spelled out.
	spelled := []struct {
		code corenet.LoopBackend
		vars map[string]string
		want loopPlan
	}{
		{corenet.LoopBackendAuto, nil, loopPlan{backend: corenet.LoopBackendAuto, fallback: true}},
		{corenet.LoopBackendAuto, map[string]string{corenet.EnvEngine: "epoll"}, loopPlan{backend: corenet.LoopBackendEpoll, fallback: true}},
		{corenet.LoopBackendAuto, map[string]string{corenet.EnvDisable: "epoll"}, loopPlan{backend: corenet.LoopBackendIOURing, fallback: true}},
		{corenet.LoopBackendAuto, map[string]string{corenet.EnvEngine: "goroutine"}, loopPlan{backend: corenet.LoopBackendPortable}},
		{corenet.LoopBackendAuto, map[string]string{corenet.EnvEngine: "nope"}, loopPlan{backend: corenet.LoopBackendPortable}},
		{corenet.LoopBackendIOURing, map[string]string{corenet.EnvDisable: "iouring"}, loopPlan{backend: corenet.LoopBackendEpoll}},
		{corenet.LoopBackendEpoll, map[string]string{corenet.EnvEngine: "iouring"}, loopPlan{backend: corenet.LoopBackendEpoll}},
		{corenet.LoopBackendPortable, map[string]string{corenet.EnvEngine: "iouring"}, loopPlan{backend: corenet.LoopBackendPortable}},
		// io_uring receives into provided buffers only: without them it is never set up.
		{corenet.LoopBackendAuto, map[string]string{corenet.EnvDisable: "uring-pbuf"}, loopPlan{backend: corenet.LoopBackendEpoll, fallback: true}},
		{corenet.LoopBackendIOURing, map[string]string{corenet.EnvDisable: "uring-pbuf"}, loopPlan{backend: corenet.LoopBackendEpoll}},
		// Another kernel capability leaves the backend as it was.
		{corenet.LoopBackendAuto, map[string]string{corenet.EnvDisable: "uring-direct,tcp-nodelay"}, loopPlan{backend: corenet.LoopBackendAuto, fallback: true}},
		// A kernel capability is no engine: KITSUNIUM_NET_ENGINE takes it as unknown.
		{corenet.LoopBackendAuto, map[string]string{corenet.EnvEngine: "uring-pbuf"}, loopPlan{backend: corenet.LoopBackendPortable}},
	}
	for _, tc := range spelled {
		env := readOperator(envOf(tc.vars))
		if got := env.plan(tc.code, true); got.backend != tc.want.backend || got.fallback != tc.want.fallback {
			t.Errorf("%v under %v = %+v, want %+v", tc.code, tc.vars, got, tc.want)
		}
	}
}

// TestOperator_servesTheLoopGroupBelowTheCeiling starts a loop group under
// each switch and reads the backend it runs on and the override in State: an
// environment that rules io_uring out yields a group whose probe never
// started (ProbeStage "epoll"), and a capped group is not a fallback.
func TestOperator_servesTheLoopGroupBelowTheCeiling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		vars  map[string]string
		linux corenet.LoopBackend
	}{
		{map[string]string{corenet.EnvEngine: "epoll"}, corenet.LoopBackendEpoll},
		{map[string]string{corenet.EnvDisable: "iouring"}, corenet.LoopBackendEpoll},
		{map[string]string{corenet.EnvEngine: "portable"}, corenet.LoopBackendPortable},
		{map[string]string{corenet.EnvEngine: "goroutine"}, corenet.LoopBackendPortable},
		{map[string]string{corenet.EnvDisable: "iouring,epoll"}, corenet.LoopBackendPortable},
		{map[string]string{corenet.EnvEngine: "unknown"}, corenet.LoopBackendPortable},
	}
	for _, tc := range cases {
		t.Run(tc.vars[corenet.EnvEngine]+"|"+tc.vars[corenet.EnvDisable], func(t *testing.T) {
			t.Parallel()
			srv := newServer(envOf(tc.vars))
			t.Cleanup(func() { swallowErr(srv.Close()) })
			srv.Group("loop", Listen("tcp", "127.0.0.1:0")).HandleLoop(&loopLineH{})
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			st := srv.State()
			want := tc.linux
			if runtime.GOOS != "linux" {
				want = corenet.LoopBackendPortable
			}
			ls := st.Loops[0]
			if ls.Backend != want || ls.Degraded {
				t.Fatalf("backend %v degraded %v (%q), want %v", ls.Backend, ls.Degraded, ls.DegradedReason, want)
			}
			if want == corenet.LoopBackendEpoll && ls.ProbeStage != "epoll" {
				t.Fatalf("probe stage %q: io_uring was probed", ls.ProbeStage)
			}
			if st.Override.Source != "environment" || st.Override.Raw == "" {
				t.Fatalf("override = %+v", st.Override)
			}
			if st.Fallbacks.Count != 0 {
				t.Fatalf("an override counted as a fallback: %+v", st.Fallbacks)
			}
			if c := loopDial(t, st.Listeners[0].Address); c == nil {
				t.Fatal("no connection")
			}
		})
	}
}

// TestOperator_neverTouchesTheGoroutineEngine starts a goroutine-engine group
// under the lowest switches: it serves, with no fault but the unknown token's,
// and the environment never touches TLS, Plaintext or a limit.
func TestOperator_neverTouchesTheGoroutineEngine(t *testing.T) {
	t.Parallel()
	srv := newServer(envOf(map[string]string{corenet.EnvEngine: "goroutine", corenet.EnvDisable: "iouring,epoll"}))
	t.Cleanup(func() { swallowErr(srv.Close()) })
	group := srv.Group("conn", Listen("tcp", "127.0.0.1:0"), MaxConns(3), Plaintext())
	group.HandleFunc(func(_ context.Context, c corenet.Conn) error { return nil })
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if group.limits.MaxConns != 3 || !group.kinds.has(corenet.OptionPlaintext) {
		t.Fatalf("the environment changed the group: %+v", group.limits)
	}
	if st := srv.State(); st.Degraded() || st.Override.Applied != corenet.CapabilityGoroutine {
		t.Fatalf("state = %+v", st)
	}
}

// TestOperator_kernelSwitches pins what a loop group is told is off: the
// kernel capabilities KITSUNIUM_NET_DISABLE lists, once each, beside the
// engines in Override.Disabled; every one of them under an unknown token,
// which leaves no optional capability (R5); none otherwise.
func TestOperator_kernelSwitches(t *testing.T) {
	t.Parallel()
	env := readOperator(envOf(map[string]string{corenet.EnvDisable: "uring-direct,epoll,uring-direct,tcp-nodelay"}))
	p := env.plan(corenet.LoopBackendAuto, true)
	if !slices.Equal(p.disabled, []corenet.Capability{corenet.CapabilityURingDirect, corenet.CapabilityTCPNoDelay}) || p.disabledBy != corenet.EnvDisable {
		t.Fatalf("listed: %v by %q", p.disabled, p.disabledBy)
	}
	if want := []corenet.Capability{corenet.CapabilityURingDirect, corenet.CapabilityEpoll, corenet.CapabilityTCPNoDelay}; !slices.Equal(env.override.Disabled, want) {
		t.Fatalf("Override.Disabled = %v, want %v", env.override.Disabled, want)
	}
	if len(env.faults) != 0 {
		t.Fatalf("faults = %v", env.faults)
	}
	unknown := readOperator(envOf(map[string]string{corenet.EnvDisable: "uring-sqpoll"}))
	q := unknown.plan(corenet.LoopBackendAuto, true)
	kernel := 0
	for _, e := range capabilityRegistry {
		if e.kernel {
			kernel++
		}
	}
	if len(q.disabled) != kernel || q.disabledBy != unknownDisables || q.backend != corenet.LoopBackendPortable {
		t.Fatalf("unknown token: %d off of %d by %q on %v", len(q.disabled), kernel, q.disabledBy, q.backend)
	}
	none := readOperator(envOf(nil))
	if p := none.plan(corenet.LoopBackendAuto, true); len(p.disabled) != 0 {
		t.Fatalf("no variable: %v off", p.disabled)
	}
}
