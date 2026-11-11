//go:build linux

package loop

import (
	"io"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// rowsGroup starts a group on backend b over a fresh loopback listener, with
// mod applied to its Config, and returns it with a duplicate of the
// listener's descriptor: the socket the loop set its options on, which every
// accepted socket inherits (#971).
func rowsGroup(t *testing.T, b backendCase, mod func(*Config)) (*Group, int) {
	t.Helper()
	fd, _ := listenFD(t)
	lfd, err := syscall.Dup(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(lfd) })
	cfg := Config{
		Group: "rows", Value: corenet.LoopValue{Backend: b.backend, Loops: 2},
		Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard,
	}
	if mod != nil {
		mod(&cfg)
	}
	g, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, lfd
}

// sockopt reads an integer socket option, failing the test when the kernel
// refuses it.
func sockopt(t *testing.T, fd, level, opt int) int {
	t.Helper()
	v, err := syscall.GetsockoptInt(fd, level, opt)
	if err != nil {
		t.Fatalf("getsockopt(%d, %d): %v", level, opt, err)
	}
	return v
}

// TestListenerRows_keepAliveDefault holds the stdlib's keepalive, the one the
// goroutine engine's accepted sockets get, on a loop group's listener:
// SO_KEEPALIVE on, 15 s idle, 15 s interval, 9 probes, the row on.
func TestListenerRows_keepAliveDefault(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		g, lfd := rowsGroup(t, b, nil)
		if v := sockopt(t, lfd, syscall.SOL_SOCKET, syscall.SO_KEEPALIVE); v != 1 {
			t.Fatalf("SO_KEEPALIVE = %d, want 1", v)
		}
		for opt, want := range map[int]int{syscall.TCP_KEEPIDLE: 15, syscall.TCP_KEEPINTVL: 15, syscall.TCP_KEEPCNT: 9} {
			if v := sockopt(t, lfd, syscall.IPPROTO_TCP, opt); v != want {
				t.Errorf("TCP option %d = %d, want %d", opt, v, want)
			}
		}
		if row := capRow(t, g.State(), corenet.CapabilityTCPKeepAlive); row.Status != statusOn || row.Class != classKernel {
			t.Fatalf("tcp-keepalive = %+v, want on, a-kernel", row)
		}
	})
}

// TestListenerRows_keepAliveSwitches holds the other readings of the row: a
// caller's period, a negative one that sets nothing, and the operator's switch,
// which leaves the socket as the kernel has it and the row disabled.
func TestListenerRows_keepAliveSwitches(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		for _, c := range []struct {
			name   string
			mod    func(*Config)
			alive  int
			idle   int
			status string
		}{
			{"period", func(c *Config) { c.KeepAlive = 30 * time.Second }, 1, 30, statusOn},
			{"subsecond rounds up", func(c *Config) { c.KeepAlive = 1500 * time.Millisecond }, 1, 2, statusOn},
			{"negative", func(c *Config) { c.KeepAlive = -1 }, 0, 0, statusAvailable},
			{"disabled", func(c *Config) { c.Disabled = []corenet.Capability{corenet.CapabilityTCPKeepAlive} }, 0, 0, statusDisabled},
		} {
			t.Run(c.name, func(t *testing.T) {
				g, lfd := rowsGroup(t, b, c.mod)
				if v := sockopt(t, lfd, syscall.SOL_SOCKET, syscall.SO_KEEPALIVE); v != c.alive {
					t.Fatalf("SO_KEEPALIVE = %d, want %d", v, c.alive)
				}
				if c.idle != 0 {
					if v := sockopt(t, lfd, syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE); v != c.idle {
						t.Fatalf("TCP_KEEPIDLE = %d, want %d", v, c.idle)
					}
				}
				if row := capRow(t, g.State(), corenet.CapabilityTCPKeepAlive); row.Status != c.status {
					t.Fatalf("tcp-keepalive = %+v, want %s", row, c.status)
				}
			})
		}
	})
}

// TestListenerRows_userTimeout holds TCP_USER_TIMEOUT: the caller's bound in
// milliseconds on the listener, nothing without one, nothing under the switch.
func TestListenerRows_userTimeout(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		for _, c := range []struct {
			name   string
			mod    func(*Config)
			ms     int
			status string
		}{
			{"one second", func(c *Config) { c.UserTimeout = time.Second }, 1000, statusOn},
			{"rounds up", func(c *Config) { c.UserTimeout = 1500 * time.Microsecond }, 2, statusOn},
			{"none", nil, 0, statusAvailable},
			{"disabled", func(c *Config) {
				c.UserTimeout = time.Second
				c.Disabled = []corenet.Capability{corenet.CapabilityTCPUserTimeout}
			}, 0, statusDisabled},
		} {
			t.Run(c.name, func(t *testing.T) {
				g, lfd := rowsGroup(t, b, c.mod)
				if v := sockopt(t, lfd, syscall.IPPROTO_TCP, tcpUserTimeout); v != c.ms {
					t.Fatalf("TCP_USER_TIMEOUT = %d, want %d", v, c.ms)
				}
				if row := capRow(t, g.State(), corenet.CapabilityTCPUserTimeout); row.Status != c.status || row.Class != classKernel {
					t.Fatalf("tcp-user-timeout = %+v, want %s, a-kernel", row, c.status)
				}
			})
		}
	})
}

// TestListenerRows_inherited holds what #971 rests on: a socket accepted on a
// listener the loop configured carries its options, the kernel having cloned
// them from it. It accepts with accept4 on a duplicate of the listener, apart
// from the loop's own accepts.
func TestListenerRows_inherited(t *testing.T) {
	fd, addr := listenFD(t)
	defer syscall.Close(fd)
	cfg := Config{KeepAlive: 20 * time.Second, UserTimeout: 3 * time.Second, Listeners: []Listener{{FD: fd}}, Value: corenet.LoopValue{NotsentLowat: 64 << 10}}
	_, res := prepareListeners(&cfg, newSwitches(&cfg))
	for _, r := range res {
		if r.name != corenet.CapabilityTCPDeferAccept && r.status != statusOn {
			t.Fatalf("row %+v, want on", r)
		}
	}
	dial(t, addr)
	nfd, _, err := syscall.Accept4(fd, syscall.SOCK_CLOEXEC)
	if err != nil {
		t.Fatalf("accept4: %v", err)
	}
	defer syscall.Close(nfd)
	if v := sockopt(t, nfd, syscall.IPPROTO_TCP, tcpNotsentLowat); v != 64<<10 {
		t.Errorf("accepted TCP_NOTSENT_LOWAT = %d, want %d (#857)", v, 64<<10)
	}
	if v := sockopt(t, nfd, syscall.SOL_SOCKET, syscall.SO_KEEPALIVE); v != 1 {
		t.Errorf("accepted SO_KEEPALIVE = %d, want 1", v)
	}
	if v := sockopt(t, nfd, syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE); v != 20 {
		t.Errorf("accepted TCP_KEEPIDLE = %d, want 20", v)
	}
	if v := sockopt(t, nfd, syscall.IPPROTO_TCP, tcpUserTimeout); v != 3000 {
		t.Errorf("accepted TCP_USER_TIMEOUT = %d, want 3000", v)
	}
}

// TestListenerRows_deferAccept holds the opt-in: without it a peer that
// connects is opened at once; with it, and a read bound, the group sees nothing
// of a peer that sends nothing until it does, and the row reads on. A group
// with no read bound leaves the option unset.
func TestListenerRows_deferAccept(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		t.Run("not opted into", func(t *testing.T) {
			g, lfd := rowsGroup(t, b, func(c *Config) { c.ReadTimeout = 2 * time.Second })
			if v := sockopt(t, lfd, syscall.IPPROTO_TCP, syscall.TCP_DEFER_ACCEPT); v != 0 {
				t.Fatalf("TCP_DEFER_ACCEPT = %d, want 0", v)
			}
			if row := capRow(t, g.State(), corenet.CapabilityTCPDeferAccept); row.Status == statusOn || row.Class != classCode {
				t.Fatalf("tcp-defer-accept = %+v, want an a-code row that is not on", row)
			}
		})
		t.Run("no read bound", func(t *testing.T) {
			g, lfd := rowsGroup(t, b, func(c *Config) {
				c.Value.Capabilities = []corenet.Capability{corenet.CapabilityTCPDeferAccept}
			})
			if v := sockopt(t, lfd, syscall.IPPROTO_TCP, syscall.TCP_DEFER_ACCEPT); v != 0 {
				t.Fatalf("TCP_DEFER_ACCEPT = %d, want 0", v)
			}
			if row := capRow(t, g.State(), corenet.CapabilityTCPDeferAccept); row.Status == statusOn {
				t.Fatalf("tcp-defer-accept = %+v with no read bound", row)
			}
		})
		t.Run("opted into", func(t *testing.T) {
			fd, addr := listenFD(t)
			h := &lineH{}
			opened := func() int { h.mu.Lock(); defer h.mu.Unlock(); return h.opens }
			g, err := Start(Config{
				Group: "defer", Value: corenet.LoopValue{
					Backend: b.backend, Loops: 1, Capabilities: []corenet.Capability{corenet.CapabilityTCPDeferAccept},
				},
				ReadTimeout: 2 * time.Second, Handler: h, Listeners: []Listener{{FD: fd}}, Log: io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = g.Close() })
			row := capRow(t, g.State(), corenet.CapabilityTCPDeferAccept)
			// A kernel that refuses the option says so: the opt-in is then a
			// fault, the group serves as before, and there is nothing to defer.
			if row.Status == statusMissing {
				t.Skipf("tcp-defer-accept refused by %s: %s", kernelRelease(), row.Reason)
			}
			if row.Status != statusOn {
				t.Fatalf("tcp-defer-accept = %+v, want on", row)
			}
			c := dial(t, addr)
			time.Sleep(600 * time.Millisecond)
			if n := opened(); n != 0 {
				t.Fatalf("OnOpen fired %d time(s) for a peer that sent nothing", n)
			}
			if _, err := c.Write([]byte("hi\n")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "OnOpen once the peer sent", func() bool { return opened() == 1 })
			buf := make([]byte, 3)
			if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hi\n" {
				t.Fatalf("echo = %q, %v", buf, err)
			}
		})
	})
}
