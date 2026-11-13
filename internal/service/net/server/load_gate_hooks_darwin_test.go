//go:build darwin

// The merge gate's L1 on darwin (#976): the goroutine engine against the loop
// on kqueue, named, and kqueue against itself, the macOS runner's
// calibration. The bench-darwin job of net-kernel-gate.yml runs them and
// tools/netgate judges the kqueue and cal-L1-kqueue rows alone (-only).

package server_test

import (
	"bufio"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// loadL1Pairs are L1's pairs here.
var loadL1Pairs = [][2]string{{"goroutine", "loop-kqueue"}, {"loop-kqueue", "loop-kqueue"}}

// serveLoadOS serves no load of darwin's own.
func serveLoadOS(*testing.T, *server.StreamGroup, string, string) bool { return false }

// loadGroupOptionOS names no engine of darwin's own.
func loadGroupOptionOS(string, string) (server.GroupOption, bool) { return nil, false }

// loadChildEnvOS leaves the child's environment as it is.
func loadChildEnvOS(_ string, env []string) []string { return env }

// reportLoadRows has no row to report here.
func reportLoadRows(string, corenet.StateValue) {}

// TestLoadL1Kqueue_wired serves L1 on kqueue in its child for a few requests
// and reads its counters: the darwin pair is wired before the job measures it.
func TestLoadL1Kqueue_wired(t *testing.T) {
	child := startLoadChild(t, "L1", "goroutine", "loop-kqueue")
	before := child.sample(t)
	for _, addr := range child.addrs {
		conn, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(conn)
		for range 3 {
			if err := loadRoundTrip(conn, br, loadL1Request); err != nil {
				t.Fatalf("%s: %v", addr, err)
			}
		}
		_ = conn.Close()
	}
	if after := child.sample(t); after[0] <= before[0] {
		t.Fatalf("CPU %v then %v: want it grown", before, after)
	}
}
