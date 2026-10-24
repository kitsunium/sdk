//go:build linux && !race

// Package loop — the idle-connection memory budget, held by hand in the
// race-off lane (ADR 0186 §D7): the race detector's shadow memory would
// measure itself.
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
			cmd.Env = append(os.Environ(), clientsEnv+"="+addr)
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || line != "ready\n" {
				t.Fatalf("clients: %q %v", line, err)
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

// runIdleClients is the child: it dials idleConns connections, says ready,
// and holds them until its standard input closes.
func runIdleClients(addr string) int {
	conns := make([]stdnet.Conn, 0, idleConns)
	for range idleConns {
		c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			fmt.Println("dial:", err)
			return 1
		}
		conns = append(conns, c)
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	for _, c := range conns {
		_ = c.Close()
	}
	return 0
}

func init() {
	idleClients = runIdleClients
}
