//go:build linux

// The merge gate's loads Linux has of its own: L1big (load_big_linux_test.go)
// and Sparse over the veth pair (load_sparse_veth_linux_test.go).

package server_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// loadL1Pairs are L1's pairs here: the goroutine engine against the loop on
// the best Linux backend (P6), and the loop against itself (cal-L1).
var loadL1Pairs = [][2]string{{"goroutine", "loop"}, {"loop", "loop"}}

// serveLoadOS serves engine of a load Linux has of its own, reporting
// whether it was one.
func serveLoadOS(t *testing.T, g *server.StreamGroup, load, engine string) bool {
	switch {
	case load == loadBig:
		serveBigLoad(t, g, engine)
	case load == loadSparse && (engine == "loop" || engine == "loop-busypoll"):
		g.HandleLoop(http1.HandleLoop(sparseChain(t), http1.Parser{}))
	default:
		return false
	}
	return true
}

// loadGroupOptionOS is the group option of an engine of such a load.
func loadGroupOptionOS(load, engine string) (server.GroupOption, bool) {
	switch load {
	case loadBig:
		return bigGroupOption(engine), true
	case loadSparse:
		return sparseGroupOption(engine), true
	}
	return nil, false
}

// loadChildEnvOS is the child's environment for load: L1big's listens with
// plain TCP (bigChildEnv).
func loadChildEnvOS(load string, env []string) []string {
	if load == loadBig {
		return bigChildEnv(env)
	}
	return env
}

// reportLoadRows writes the capability rows a load's engine B depends on.
func reportLoadRows(load string, st corenet.StateValue) {
	if load == loadBig || load == loadSparse {
		reportRows(st, corenet.CapabilityURingSendZC, corenet.CapabilityBusyPoll)
	}
}
