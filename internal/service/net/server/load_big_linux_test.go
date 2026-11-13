//go:build linux

// L1big: L1's request answered with a 64 KiB body, the size a copy is worth
// avoiding. Its pairs measure the two ways the loop answers a large body
// without copying it into the slab and again into the socket: a flight sent
// by IORING_OP_SEND_ZC against the same flight sent by IORING_OP_SEND
// (loop:loop-sendzc, #969), and a file sent by sendfile or splice from
// http1.Files against net/static's tree on the goroutine engine
// (goroutine-static:loop-files, #982). Both are reported by tools/netgate,
// not gated, until their ratios have been read on the runner.

package server_test

import (
	"bufio"
	"bytes"
	"fmt"
	stdnet "net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
	"github.com/kitsunium/sdk/internal/service/net/static"
)

const (
	// loadBig names the load.
	loadBig = "L1big"
	// loadBigBodySize is its answer: 64 KiB, the body #969 and #982 name.
	loadBigBodySize = 64 << 10
	// loadBigScale divides b.N: a round of L1big sends one request in eight
	// of L1's, so its bytes, sixty-four times L1's a request, stay within
	// the bench job's time.
	loadBigScale = 8
	// loadBigRoute is the file tree's route on the loop.
	loadBigRoute = 1
	// loadBigName is the file both file engines serve, under /assets/.
	loadBigName = "big.bin"
)

// loadBigRequest is L1big's request, the same on every engine.
var loadBigRequest = []byte("GET /assets/" + loadBigName + " HTTP/1.1\r\nHost: netgate\r\n\r\n")

// loadBigBody is the answer the in-memory engines send.
var loadBigBody = bytes.Repeat([]byte("b"), loadBigBodySize)

// bigTreeRoot is the child's file tree: assets/big.bin, loadBigBody's bytes,
// written once under the child test's own directory.
var bigTreeRoot string

// bigTree returns the child's tree, writing it on the first call.
func bigTree(t *testing.T) string {
	t.Helper()
	if bigTreeRoot != "" {
		return bigTreeRoot
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", loadBigName), loadBigBody, 0o600); err != nil {
		t.Fatal(err)
	}
	bigTreeRoot = root
	return root
}

// serveBigLoad declares engine's handler of L1big on g: the 64 KiB body from
// memory on the loop, with or without zero-copy sends, or the file from the
// tree, on the goroutine engine through net/static or on the loop through
// http1.Files.
func serveBigLoad(t *testing.T, g *server.StreamGroup, engine string) {
	switch engine {
	case "loop", "loop-sendzc":
		g.HandleLoop(http1.HandleLoop(corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
			res.Status, res.Body = http.StatusOK, loadBigBody
		}), http1.Parser{}))
	case "goroutine-static":
		h, err := static.NewHandler(os.DirFS(bigTree(t)), static.Config{})
		if err != nil {
			t.Fatal(err)
		}
		g.HandleHTTP(h)
	case "loop-files":
		routes, err := http1.Routes(http1.RouteValue{ID: loadBigRoute, Path: "/assets/", Prefix: true})
		if err != nil {
			t.Fatal(err)
		}
		files, err := http1.Files(loadBigRoute, os.DirFS(filepath.Join(bigTree(t), "assets")), static.Config{})
		if err != nil {
			t.Fatal(err)
		}
		app := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Status = http.StatusNotFound })
		g.HandleLoop(http1.HandleLoop(corenet.Chain[corenet.HTTP1Handler](app, routes, files), http1.Parser{}))
	default:
		t.Fatalf("no engine %q on load %q", engine, loadBig)
	}
}

// bigGroupOption is an L1big engine's group option: the goroutine engine, or
// a loop on LoopsAuto as L1's, uring-send-zc opted into for loop-sendzc, and
// for loop-files a worker queue as deep as the load's connections.
func bigGroupOption(engine string) server.GroupOption {
	if engine == "goroutine-static" {
		return server.Plaintext()
	}
	v := corenet.LoopValue{Backend: corenet.LoopBackendAuto, Loops: corenet.LoopsAuto}
	switch engine {
	case "loop-sendzc":
		v.Capabilities = []corenet.Capability{corenet.CapabilityURingSendZC}
	case "loop-files":
		//: a lane's queue to its worker holds every request in flight: the
		//: default 256 would answer the rest 503 (LoopOutboundFull), which
		//: is a refusal measured, not a file sent.
		v.Outbound = loadConns
	}
	return server.Loop(v)
}

// reportRows writes, on the child's stderr, the rows of names of every loop
// group, so a run's log says whether loop-sendzc sent zero-copy and
// loop-busypoll polled: a kernel that refuses either reads missing, and the
// pair then measures the same engine twice.
func reportRows(st corenet.StateValue, names ...corenet.Capability) {
	for _, l := range st.Loops {
		for _, c := range l.Capabilities {
			if slices.Contains(names, c.Name) {
				fmt.Fprintf(os.Stderr, "netgate: %s %s %s %s %s\n", l.Group, l.Backend, c.Name, c.Status, c.Reason)
			}
		}
	}
}

// bigChildEnv is env with multipathtcp=0 added to GODEBUG: every engine of
// L1big's child listens with plain TCP. loop-sendzc binds plain TCP itself, as
// every group opted into uring-send-zc does (#969) — a connection accepted
// from Go's default listener, MPTCP since Go 1.24, refuses IORING_OP_SEND_ZC
// —; the variable gives its partner, loop, the same transport, so the pair
// differs by the send alone.
func bigChildEnv(env []string) []string {
	debug := "multipathtcp=0"
	out := env[:0:0]
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GODEBUG="); ok {
			if v != "" {
				debug = v + "," + debug
			}
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GODEBUG="+debug)
}

// BenchmarkLoadL1big is L1big paired: the loop's copying send against its
// zero-copy one (#969), and net/static on the goroutine engine against
// http1.Files on the loop (#982), both at 64 KiB.
func BenchmarkLoadL1big(b *testing.B) {
	for _, p := range [][2]string{{"loop", "loop-sendzc"}, {"goroutine-static", "loop-files"}} {
		b.Run("pair="+p[0]+":"+p[1], func(b *testing.B) {
			benchPairedSized(b, loadBig, p[0], p[1], loadBigRequest, loadBigBodySize, loadBigScale)
		})
	}
}

// TestLoadL1big_wired serves each engine of L1big in its child for a few
// requests, each answer 64 KiB, and reads its counters, so a harness that no
// longer serves its load fails the suite, not only the bench.
func TestLoadL1big_wired(t *testing.T) {
	for _, engine := range []string{"loop", "loop-sendzc", "goroutine-static", "loop-files"} {
		t.Run(engine, func(t *testing.T) {
			child := startLoadChild(t, loadBig, engine)
			conn, err := stdnet.DialTimeout("tcp", child.addrs[0], 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			before := child.sample(t)
			br := bufio.NewReader(conn)
			for range 3 {
				if err := loadRoundTripN(conn, br, loadBigRequest, loadBigBodySize); err != nil {
					t.Fatal(err)
				}
			}
			after := child.sample(t)
			if len(after) != 1 || after[0] <= before[0] {
				t.Fatalf("CPU %v then %v: want one counter, grown by three requests", before, after)
			}
		})
	}
}
