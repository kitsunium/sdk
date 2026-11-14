//go:build (linux || darwin || freebsd || openbsd || netbsd || dragonfly) && !race

// Package loop — the idle-connection memory budget, held by hand in the
// race-off lane (ADR 0186 §D7): the race detector's shadow memory would
// measure itself. It runs on every native backend, kqueue on the darwin lane
// and the BSD guests included (ADR 0222).
package loop

import (
	"bufio"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"os/exec"
	"runtime"
	"runtime/metrics"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// idleConnHeapBudget is the most Go heap one idle connection may cost on a
	// native backend, the slot table's growth included: its control state is
	// a pointer-free slot and a timer link. The design fixes it in the
	// package's documentation.
	idleConnHeapBudget = 256
	// idleConns is how many idle connections the test holds.
	idleConns = 10000
	// idleDialWave is how many connections the child dials before it waits
	// for the server to have accepted every one: SOMAXCONN, the listen queue
	// darwin and the BSDs clamp any backlog to by default. The 4096 listenFD
	// asks for is not what a host grants: FreeBSD's kern.ipc.soacceptqueue
	// is 128, and past one and a half times that a completed handshake is
	// answered by a reset, so 10 000 dials racing the loop's accepts made the
	// test measure the guest's queue and its scheduler (#1407).
	idleDialWave = syscall.SOMAXCONN
	// clientsEnv marks the re-executed client process and names the address.
	clientsEnv = "KITSUNIUM_LOOP_IDLE_CLIENTS"
)

// heapLive reads the live heap after two collections.
func heapLive() uint64 {
	runtime.GC()
	runtime.GC()
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// TestMemory_idleConnection opens idleConns connections from a child process
// — so no client byte is counted here — and holds the per-connection Go
// heap, measured after two collections through runtime/metrics, to the
// budget. An idle connection holds no slab chunk: its mapped cost is zero.
func TestMemory_idleConnection(t *testing.T) {
	if lim := descriptorLimit(); lim < 2*idleConns+256 {
		t.Skipf("RLIMIT_NOFILE %d is below what %d connections need", lim, idleConns)
	}
	for _, b := range backendsPresent() {
		t.Run(b.name, func(t *testing.T) {
			if err := probeBackend(b.backend); err != nil {
				t.Skipf("%s not present: %v", b.name, err)
			}
			h := &lineH{}
			g, addr := serve(t, b, h, nil)
			before := heapLive()
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			// The child's role is set once, any inherited one removed first, and
			// the role relaunches nothing.
			cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, clientsEnv+"=") }), clientsEnv+"="+addr)
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
			out := bufio.NewReader(stdout)
			// Each wave is accepted whole before the next is dialed, so the
			// listen queue never holds more than idleDialWave connections.
			for dialed := int64(idleDialWave); ; dialed += idleDialWave {
				line, err := out.ReadString('\n')
				if err != nil || (line != "wave\n" && line != "ready\n") {
					t.Fatalf("clients: %q %v", line, err)
				}
				if line == "ready\n" {
					break
				}
				waitFor(t, "a wave accepted", func() bool { return g.Live() == dialed })
				if _, err := io.WriteString(stdin, "\n"); err != nil {
					t.Fatalf("clients: %v", err)
				}
			}
			waitFor(t, "every open", func() bool { return g.Live() == idleConns })
			after := heapLive()
			per := float64(int64(after)-int64(before)) / idleConns
			mapped := 0
			for _, lp := range g.loops {
				mapped += lp.slab.inUse
			}
			t.Logf("%s: %d idle connections, %.1f B of Go heap each, %d B of slab held", b.name, idleConns, per, mapped)
			if per > idleConnHeapBudget {
				t.Fatalf("an idle connection costs %.1f B of Go heap, budget %d", per, idleConnHeapBudget)
			}
			if mapped != 0 {
				t.Fatalf("idle connections hold %d B of slab, want none", mapped)
			}
		})
	}
}

// runIdleClients is the child: it dials idleConns connections idleDialWave
// at a time — saying wave after each and waiting for a line on its standard
// input before the next —, says ready, and holds them until its standard
// input closes.
func runIdleClients(addr string) int {
	conns := make([]stdnet.Conn, 0, idleConns)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	in := bufio.NewReader(os.Stdin)
	for len(conns) < idleConns {
		for range min(idleDialWave, idleConns-len(conns)) {
			c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				fmt.Println("dial:", err)
				return 1
			}
			conns = append(conns, c)
		}
		if len(conns) == idleConns {
			break
		}
		fmt.Println("wave")
		if _, err := in.ReadString('\n'); err != nil {
			return 1
		}
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, in)
	return 0
}

func init() {
	idleClients = runIdleClients
}
