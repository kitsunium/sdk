//go:build linux

// Package loop — the kernel capabilities' conformance (ADR 0189): the
// report, each class A-kernel switch, each class A-code opt-in through the
// suite's echo, large response and close, and the refusals.
package loop

import (
	"bufio"
	"bytes"
	"io"
	"strconv"
	"strings"
	"syscall"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// capRow is st's row for c, failing the test when there is none.
func capRow(t *testing.T, st corenet.LoopStateValue, c corenet.Capability) corenet.CapabilityValue {
	t.Helper()
	for _, row := range st.Capabilities {
		if row.Name == c {
			return row
		}
	}
	t.Fatalf("no capability row %q in %+v", c, st.Capabilities)
	return corenet.CapabilityValue{}
}

// probeCap starts a group on backend with c opted into, and fails with
// errCapAbsent unless its row reads on: the lanes' requiredCaps probe.
func probeCap(backend corenet.LoopBackend, c corenet.Capability) func() error {
	return func() error {
		v := corenet.LoopValue{Backend: backend}
		if class, _ := capClassOf(c); class == classCode {
			v.Capabilities = []corenet.Capability{c}
		}
		g, err := Start(Config{Group: "probe", Value: v, Handler: &lineH{}, Log: io.Discard})
		if err != nil {
			return err
		}
		st := g.State()
		if err := g.Close(); err != nil {
			return err
		}
		for _, row := range st.Capabilities {
			if row.Name == c && row.Status == statusOn {
				return nil
			}
			if row.Name == c {
				return errs.Wrap(errCapAbsent, errs.WrapParams{}, errs.String("status", row.Status), errs.String("reason", row.Reason))
			}
		}
		return errCapAbsent
	}
}

func init() {
	for _, c := range []corenet.Capability{
		corenet.CapabilityURingMultishotRecv, corenet.CapabilityURingPbuf,
		corenet.CapabilityURingPbufInc, corenet.CapabilityURingDeferTaskrun, corenet.CapabilityURingNoSQArray,
		corenet.CapabilityURingRingFDs, corenet.CapabilityURingSQRewind,
	} {
		capProbes[string(c)] = probeCap(corenet.LoopBackendIOURing, c)
	}
	capProbes[string(corenet.CapabilityEpollExclusive)] = probeCap(corenet.LoopBackendEpoll, corenet.CapabilityEpollExclusive)
}

// echoBigClose is the suite's short exchange: an echo, a 2 MiB response
// byte-exact, and a close the peer reads as the end.
func echoBigClose(t *testing.T, addr string) {
	t.Helper()
	c := dial(t, addr)
	const n = 2 << 20
	_, _ = c.Write([]byte("hi\nBIG " + strconv.Itoa(n) + "\n"))
	r := bufio.NewReader(c)
	if line, err := r.ReadString('\n'); err != nil || line != "hi\n" {
		t.Fatalf("echo = %q, %v", line, err)
	}
	got := make([]byte, n)
	if _, err := io.ReadFull(r, got); err != nil || !bytes.Equal(got, pattern(n)) {
		t.Fatalf("large response: %v", err)
	}
	_, _ = c.Write([]byte("CLOSE\n"))
	if _, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("after CLOSE = %v, want EOF", err)
	}
}

// TestCaps_report holds every backend's report: one row per capability the
// loop reports, in order, each with a class and a status, none a probe
// defect, class B never on, and the logs naming what each found.
func TestCaps_report(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		g, _ := serve(t, b, &lineH{}, nil)
		st := g.State()
		if len(st.Capabilities) != len(loopCaps) {
			t.Fatalf("%d rows, want %d: %+v", len(st.Capabilities), len(loopCaps), st.Capabilities)
		}
		for i, row := range st.Capabilities {
			if row.Name != loopCaps[i].name || row.Class != loopCaps[i].class {
				t.Errorf("row %d = %s/%s, want %s/%s", i, row.Name, row.Class, loopCaps[i].name, loopCaps[i].class)
			}
			if strings.Contains(row.Reason, "probe defect") {
				t.Errorf("%s: %s", row.Name, row.Reason)
			}
			if row.Class == classB && row.Status == statusOn {
				t.Errorf("class B %s is on", row.Name)
			}
			if row.Class == classCode && row.Status == statusOn {
				t.Errorf("class A-code %s is on without an opt-in", row.Name)
			}
			t.Logf("%s %s: %s %s %s", kernelRelease(), b.name, row.Name, row.Status, row.Reason)
		}
		if b.backend == corenet.LoopBackendIOURing {
			for _, c := range []corenet.Capability{corenet.CapabilityURingMultishotRecv, corenet.CapabilityURingPbuf} {
				if row := capRow(t, st, c); row.Status != statusOn {
					t.Errorf("%s = %s on a running io_uring group", c, row.Status)
				}
			}
		}
	})
}

// TestCaps_kernelSwitches switches each class A-kernel capability off: the
// row reads disabled, the group still serves the suite's exchange, and what
// the switch changes is observable.
func TestCaps_kernelSwitches(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		for _, c := range []corenet.Capability{
			corenet.CapabilityURingMultishotRecv, corenet.CapabilityURingDirect,
			corenet.CapabilityEpollExclusive, corenet.CapabilityTCPNoDelay,
			corenet.CapabilityURingPbufInc,
		} {
			t.Run(string(c), func(t *testing.T) {
				fd, addr := listenFD(t)
				lfd, err := syscall.Dup(fd)
				if err != nil {
					t.Fatal(err)
				}
				defer syscall.Close(lfd)
				g, err := Start(Config{
					Group: "off", Value: corenet.LoopValue{Backend: b.backend, Loops: 2},
					Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard,
					Disabled: []corenet.Capability{c},
				})
				if err != nil {
					t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
				}
				defer g.Close()
				st := g.State()
				if c != corenet.CapabilityTCPNoDelay {
					if row := capRow(t, st, c); row.Status != statusDisabled || row.Reason != corenet.EnvDisable {
						t.Fatalf("%s = %+v, want disabled by %s", c, row, corenet.EnvDisable)
					}
				}
				switch c {
				case corenet.CapabilityURingDirect:
					if b.backend == corenet.LoopBackendIOURing && st.ProbeStage != "self_test+fds" {
						t.Fatalf("probe stage %q, want self_test+fds", st.ProbeStage)
					}
				case corenet.CapabilityURingPbufInc:
					if b.backend == corenet.LoopBackendIOURing && consumesIncrementally(g) {
						t.Fatal("a ring's buffers are consumed incrementally with uring-pbuf-inc switched off")
					}
				case corenet.CapabilityTCPNoDelay:
					if v, err := syscall.GetsockoptInt(lfd, syscall.IPPROTO_TCP, syscall.TCP_NODELAY); err != nil || v != 0 {
						t.Fatalf("TCP_NODELAY = %d, %v with tcp-nodelay switched off", v, err)
					}
				}
				for range 4 {
					echoBigClose(t, addr)
				}
			})
		}
	})
}

// TestCaps_pbufOff switches uring-pbuf off: auto runs on epoll, neither
// probed nor degraded, and an explicit io_uring is refused before any setup.
func TestCaps_pbufOff(t *testing.T) {
	off := []corenet.Capability{corenet.CapabilityURingPbuf}
	fd, addr := listenFD(t)
	g, err := Start(Config{Group: "auto", Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard, Disabled: off})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	st := g.State()
	if st.Backend != corenet.LoopBackendEpoll || st.Degraded || st.ProbeStage != "epoll" {
		t.Fatalf("auto with uring-pbuf off = %v degraded=%v stage=%q", st.Backend, st.Degraded, st.ProbeStage)
	}
	echoBigClose(t, addr)
	fd2, _ := listenFD(t)
	_, err = Start(Config{Group: "explicit", Value: corenet.LoopValue{Backend: corenet.LoopBackendIOURing}, Handler: &lineH{}, Listeners: []Listener{{FD: fd2}}, Log: io.Discard, Disabled: off})
	if !errs.HasCode(err, corenet.CodeLoopBackendUnavailable) {
		t.Fatalf("explicit io_uring with uring-pbuf off = %v", err)
	}
}

// TestCaps_refused holds LoopValue.Capabilities to what a group may opt
// into: an engine, a class B capability and an unknown token are refused.
func TestCaps_refused(t *testing.T) {
	for _, c := range []corenet.Capability{corenet.CapabilityIOURing, corenet.CapabilityURingSendZC, "uring-sqpoll"} {
		fd, _ := listenFD(t)
		_, err := Start(Config{Group: "r", Value: corenet.LoopValue{Capabilities: []corenet.Capability{c}}, Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard})
		if !errs.HasCode(err, corenet.CodeLoopCapabilityRefused) {
			t.Errorf("%q: %v, want LoopCapabilityRefused", c, err)
		}
	}
	if err := CheckCapabilities("ok", corenet.LoopValue{Capabilities: []corenet.Capability{corenet.CapabilityURingDeferTaskrun, corenet.CapabilityTCPNoDelay}}); err != nil {
		t.Errorf("class A opt-ins refused: %v", err)
	}
}

// codeCase opts c in on io_uring and returns the group, or skips when the
// kernel lacks c: its row then says absent, with a reason.
func codeCase(t *testing.T, mod func(*Config), cs ...corenet.Capability) (*Group, string) {
	t.Helper()
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	g, addr := serve(t, b, &lineH{}, func(cfg *Config) {
		cfg.Value.Capabilities = cs
		cfg.Value.OutputCap = 256 << 10
		if mod != nil {
			mod(cfg)
		}
	})
	st := g.State()
	for _, c := range cs {
		row := capRow(t, st, c)
		if row.Status == statusMissing {
			t.Skipf("%s absent on %s: %s", c, kernelRelease(), row.Reason)
		}
		if row.Status != statusOn {
			t.Fatalf("%s opted into = %+v, want on", c, row)
		}
	}
	return g, addr
}

// TestCaps_codeOptIns runs the suite's exchange under each class A-code
// capability, and all four together, on several rings and loops.
func TestCaps_codeOptIns(t *testing.T) {
	for _, cs := range [][]corenet.Capability{
		{corenet.CapabilityURingDeferTaskrun},
		{corenet.CapabilityURingNoSQArray},
		{corenet.CapabilityURingRingFDs},
		{corenet.CapabilityURingSQRewind},
		{corenet.CapabilityURingDeferTaskrun, corenet.CapabilityURingNoSQArray, corenet.CapabilityURingRingFDs},
		{corenet.CapabilityURingDeferTaskrun, corenet.CapabilityURingNoSQArray, corenet.CapabilityURingRingFDs, corenet.CapabilityURingSQRewind},
	} {
		var names []string
		for _, c := range cs {
			names = append(names, strings.TrimPrefix(string(c), "uring-"))
		}
		name := strings.Join(names, "+")
		t.Run(name, func(t *testing.T) {
			_, addr := codeCase(t, func(cfg *Config) { cfg.Value.Loops, cfg.Value.Rings = 2, 3 }, cs...)
			for range 8 {
				echoBigClose(t, addr)
			}
		})
	}
}
