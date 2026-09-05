// Package server — the start, accept and drain lifecycle.
package server

import (
	"context"
	stdnet "net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// newTestServer builds a server with a run context bound to the test, and closes
// it when the test ends so no accept loop outlives it.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	srv := New()
	srv.runCtx = t.Context()
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return srv
}

// noopConn is a handler that does nothing, for wiring tests.
func noopConn(context.Context, corenet.Conn) error {
	//: the connection is closed by the engine on the way out.
	return nil
}

// noopPacketHandler is its datagram counterpart.
func noopPacketHandler(context.Context, corenet.Packet) error {
	//: the datagram is done with once this returns.
	return nil
}

// Test_batchDegradation pins that only a group which ASKED for batching is
// degraded by its absence.
//
// A silent fallback is indistinguishable from a working one, which is the whole
// reason it is surfaced through State rather than logged once at startup. But
// reporting it for a group that never wanted batching would make every default
// datagram listener in the fleet look broken.
func Test_batchDegradation(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// batch is the group's configured batch size.
		batch int
	}
	tests := []tc{
		{name: "batching explicitly disabled", batch: 1},
		{name: "batching left at the default", batch: 0},
		{name: "batching explicitly requested", batch: 32},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		degraded, reason := batchDegradation(corenet.LimitsValue{BatchSize: c.batch})

		//: on a platform that batches nothing is ever degraded; elsewhere only a
		//: group that asked for more than one datagram per syscall is.
		want := !batchAvailable() && batchSize(corenet.LimitsValue{BatchSize: c.batch}) > 1
		if degraded != want {
			t.Fatalf("batchDegradation(%d) reported degraded = %v, want %v", c.batch, degraded, want)
		}
		//: the flag and the reason must agree, or State contradicts itself.
		if degraded != (reason != "") {
			t.Fatalf("degraded=%v but reason=%q — the report contradicts itself", degraded, reason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_recoverHandler pins CONTAINMENT. One malformed peer must never be able to
// take the process down, so a panicking handler costs its own connection and
// nothing more — which means the recover has to run on the goroutine that
// panicked, not somewhere that merely observes it.
func Test_recoverHandler(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// value is what the handler panics with, or nil for no panic at all.
		value any
	}
	tests := []tc{
		{name: "no panic at all"},
		{name: "a string panic", value: "handler exploded"},
		{name: "an error panic", value: errs.Wrap(corenet.ServerClosed, errs.WrapParams{})},
		//: a nil-map write and a nil dereference are what a real handler panics
		//: with, and neither is a string.
		{name: "a runtime panic", value: struct{ code int }{code: 7}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		survived := func() (ok bool) {
			defer recoverHandler()
			//: a handler that returns normally must be unaffected.
			if c.value == nil {
				//: nothing to contain.
				return true
			}
			panic(c.value)
		}()

		//: reaching this line at all is the property being pinned: the panic
		//: did not unwind past the connection that caused it.
		if c.value == nil && !survived {
			t.Fatal("a handler that never panicked was reported as contained")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_bindShards pins that a shard count above one opens SEVERAL
// listeners on one address while State reports ONE row.
//
// Several listeners on one address is the whole point of SO_REUSEPORT: the
// kernel load-balances accepts across them, so N accept loops never contend on
// one queue. N rows in State would read as N separate addresses, which is why
// only the first shard is reported — carrying the real count.
func Test_Server_bindShards(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// network and addr are the address to bind.
		network string
		addr    string
		// unixSocket puts the address in the test's temporary directory.
		unixSocket bool
		// shards is the group's requested shard count.
		shards int
		// wantCode is the refusal, or zero when the bind must succeed.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a single listener", network: "tcp", addr: "127.0.0.1:0", shards: 1},
		{name: "several shards", network: "tcp", addr: "127.0.0.1:0", shards: 4},
		{name: "auto-sizing", network: "tcp", addr: "127.0.0.1:0", shards: 0},
		//: a unix socket cannot be shared, so the count collapses to one.
		{name: "a unix socket asked to shard", network: "unix", unixSocket: true, shards: 4},
		{name: "an unserved family", network: "carrier-pigeon", addr: "nest", shards: 1, wantCode: corenet.CodeUnsupportedNetwork},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		addr := c.addr
		if c.unixSocket {
			addr = t.TempDir() + "/shard.sock"
		}
		srv := newTestServer(t)
		group := &StreamGroup{name: "api", limits: corenet.LimitsValue{Shards: c.shards}}
		group.HandleFunc(noopConn)
		target := corenet.AddressValue{Network: c.network, Addr: addr}

		err := srv.bindShards(t.Context(), group, target, group.resolved())

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("bindShards = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("bindShards = %v, want nil", err)
		}
		wantShards, _, _ := resolveShards(c.shards, c.network)
		srv.mu.RLock()
		listeners := len(srv.listeners)
		states := len(srv.states)
		reported := srv.states[0]
		srv.mu.RUnlock()
		//: one listener per shard, all on the same address.
		if listeners != wantShards {
			t.Fatalf("bindShards opened %d listeners, want %d", listeners, wantShards)
		}
		//: one ROW per address, carrying the real shard count — N rows would
		//: read as N separate addresses.
		if states != 1 {
			t.Fatalf("State reports %d rows for one address, want 1", states)
		}
		if reported.Shards != wantShards {
			t.Errorf("the row reports %d shards, want %d", reported.Shards, wantShards)
		}
		//: the kernel's chosen address, not the ":0" that was requested.
		if reported.Address == "" || reported.Address == c.addr && c.addr != "" && !c.unixSocket {
			t.Errorf("the row reports %q, want the address the kernel assigned", reported.Address)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_bindGroup pins that a group's ceiling is built ONCE, and shared.
//
// The budget is per group, not per socket: a group listening on a TCP port and a
// Unix socket, or sharded across several listeners, shares one ceiling — which
// is what an operator sizing a server actually means. Building it per address
// would silently multiply the limit by the address count.
func Test_Server_bindGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// addrs is how many addresses the group binds.
		addrs int
		// maxConns is the group's configured ceiling.
		maxConns int
		// shards is the group's requested shard count.
		shards int
	}
	tests := []tc{
		{name: "one address, no ceiling", addrs: 1},
		{name: "one address with a ceiling", addrs: 1, maxConns: 4},
		{name: "two addresses share one ceiling", addrs: 2, maxConns: 4},
		{name: "sharded addresses share one ceiling", addrs: 1, maxConns: 4, shards: 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		group := &StreamGroup{
			name:   "api",
			limits: corenet.LimitsValue{MaxConns: c.maxConns, Shards: c.shards},
		}
		group.HandleFunc(noopConn)
		for range c.addrs {
			group.addrs = append(group.addrs, corenet.AddressValue{Network: "tcp", Addr: "127.0.0.1:0"})
		}

		if err := srv.bindGroup(t.Context(), group); err != nil {
			t.Fatalf("bindGroup = %v, want nil", err)
		}

		//: one ceiling for the whole group, whatever it listens on.
		if (group.limiter != nil) != (c.maxConns > 0) {
			t.Fatalf("the group has a ceiling = %v, want %v", group.limiter != nil, c.maxConns > 0)
		}
		if group.limiter != nil && group.limiter.ceiling != c.maxConns {
			t.Errorf("the ceiling is %d, want %d — a per-address ceiling would "+
				"multiply the limit by the address count", group.limiter.ceiling, c.maxConns)
		}
		srv.mu.RLock()
		states := len(srv.states)
		srv.mu.RUnlock()
		//: one row per address, whatever the shard count.
		if states != c.addrs {
			t.Errorf("State reports %d rows for %d addresses", states, c.addrs)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_bindAll pins the two declaration mistakes a group cannot be
// started with, and that they are caught BEFORE anything binds.
//
// A group with no handler would accept connections and drop them; a group with
// neither an address nor an inherited socket listens nowhere. Both look like a
// working server from the outside, which is exactly why they are refused rather
// than tolerated.
func Test_Server_bindAll(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// groups describes each group as (hasHandler, addrCount).
		handlers []bool
		addrs    []int
		// wantCode is the refusal, or zero when every group binds.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "no groups at all"},
		{name: "one complete group", handlers: []bool{true}, addrs: []int{1}},
		{name: "several complete groups", handlers: []bool{true, true}, addrs: []int{1, 2}},
		{name: "a group with no handler", handlers: []bool{false}, addrs: []int{1}, wantCode: corenet.CodeHandlerMissing},
		{name: "a group with no address", handlers: []bool{true}, addrs: []int{0}, wantCode: corenet.CodeInvalidAddress},
		{
			//: the first mistake aborts, so the second group never binds.
			name:     "a bad group before a good one",
			handlers: []bool{false, true}, addrs: []int{1, 1}, wantCode: corenet.CodeHandlerMissing,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		groups := make([]*StreamGroup, 0, len(c.handlers))
		for i, hasHandler := range c.handlers {
			group := &StreamGroup{name: "g"}
			if hasHandler {
				group.HandleFunc(noopConn)
			}
			for range c.addrs[i] {
				group.addrs = append(group.addrs, corenet.AddressValue{Network: "tcp", Addr: "127.0.0.1:0"})
			}
			groups = append(groups, group)
		}

		err := srv.bindAll(t.Context(), groups)

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("bindAll = %v, want code %v", err, c.wantCode)
			}
			//: the refusal names the group, which is what an operator greps for.
			var named bool
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "group" {
					named = true
				}
			}
			if !named {
				t.Errorf("the refusal does not name the group: %v", errs.FieldsOf(err))
			}
			return
		}
		if err != nil {
			t.Fatalf("bindAll = %v, want nil", err)
		}
		want := 0
		for _, n := range c.addrs {
			want += n
		}
		srv.mu.RLock()
		states := len(srv.states)
		srv.mu.RUnlock()
		if states != want {
			t.Fatalf("State reports %d listeners, want %d", states, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_bindPacketAll pins the same two declaration mistakes on the
// datagram side. A group with no handler would read datagrams and drop them,
// which is worse than refusing to start: the socket answers, so nothing upstream
// notices.
func Test_Server_bindPacketAll(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// handlers and addrs describe each group.
		handlers []bool
		addrs    []int
		// wantCode is the refusal, or zero when every group binds.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "no groups at all"},
		{name: "one complete group", handlers: []bool{true}, addrs: []int{1}},
		{name: "several complete groups", handlers: []bool{true, true}, addrs: []int{1, 2}},
		{name: "a group with no handler", handlers: []bool{false}, addrs: []int{1}, wantCode: corenet.CodeHandlerMissing},
		{name: "a group with no address", handlers: []bool{true}, addrs: []int{0}, wantCode: corenet.CodeInvalidAddress},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		groups := make([]*PacketGroup, 0, len(c.handlers))
		for i, hasHandler := range c.handlers {
			group := &PacketGroup{name: "g"}
			if hasHandler {
				group.HandleFunc(noopPacketHandler)
			}
			for range c.addrs[i] {
				group.addrs = append(group.addrs, corenet.AddressValue{Network: "udp", Addr: "127.0.0.1:0"})
			}
			groups = append(groups, group)
		}

		err := srv.bindPacketAll(t.Context(), groups)

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("bindPacketAll = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("bindPacketAll = %v, want nil", err)
		}
		want := 0
		for _, n := range c.addrs {
			want += n
		}
		srv.mu.RLock()
		conns := len(srv.packetConns)
		srv.mu.RUnlock()
		if conns != want {
			t.Fatalf("bindPacketAll bound %d sockets, want %d", conns, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_bindPacketGroup pins that one read goroutine is started per
// address, each holding an in-flight token the drain waits on, and that the
// socket is reported in State with the address the kernel actually chose.
func Test_Server_bindPacketGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// addrs is how many addresses the group binds.
		addrs int
		// network is the datagram family.
		network string
		// wantCode is the refusal, or zero when the bind must succeed.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "one address", addrs: 1, network: "udp"},
		{name: "several addresses", addrs: 3, network: "udp"},
		{name: "no address at all", network: "udp"},
		{name: "a stream family", addrs: 1, network: "tcp", wantCode: corenet.CodeUnsupportedNetwork},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		group := &PacketGroup{name: "dns"}
		group.HandleFunc(noopPacketHandler)
		for range c.addrs {
			group.addrs = append(group.addrs, corenet.AddressValue{Network: c.network, Addr: "127.0.0.1:0"})
		}

		err := srv.bindPacketGroup(t.Context(), group)

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("bindPacketGroup = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("bindPacketGroup = %v, want nil", err)
		}
		srv.mu.RLock()
		conns := len(srv.packetConns)
		states := len(srv.states)
		rows := slices.Clone(srv.states)
		srv.mu.RUnlock()
		if conns != c.addrs || states != c.addrs {
			t.Fatalf("bound %d sockets and %d rows, want %d of each", conns, states, c.addrs)
		}
		for i, row := range rows {
			//: a datagram socket has no shards; reporting zero would read as a
			//: listener that is not serving.
			if row.Shards != 1 {
				t.Errorf("row %d reports %d shards, want 1", i, row.Shards)
			}
			//: the kernel's chosen port, not the ":0" that was requested.
			if row.Address == "" || row.Address == "127.0.0.1:0" {
				t.Errorf("row %d reports %q, want the address the kernel assigned", i, row.Address)
			}
			if row.Adopted {
				t.Errorf("row %d claims a bound socket was adopted", i)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_bindEverything pins the ORDER: streams first, then datagrams, so a
// mixed server's ports come up in declaration order and a failure on either side
// aborts before the other binds.
func Test_Server_bindEverything(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// streamOK and packetOK say whether each side is declared correctly.
		streamOK bool
		packetOK bool
		// wantCode is the refusal, or zero when both sides bind.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "both sides bind", streamOK: true, packetOK: true},
		//: the stream side is bound first, so its mistake is the one reported.
		{name: "a bad stream group", packetOK: true, wantCode: corenet.CodeHandlerMissing},
		{name: "a bad datagram group", streamOK: true, wantCode: corenet.CodeHandlerMissing},
		{name: "both sides wrong", wantCode: corenet.CodeHandlerMissing},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		stream := &StreamGroup{
			name:  "api",
			addrs: []corenet.AddressValue{{Network: "tcp", Addr: "127.0.0.1:0"}},
		}
		if c.streamOK {
			stream.HandleFunc(noopConn)
		}
		datagram := &PacketGroup{
			name:  "dns",
			addrs: []corenet.AddressValue{{Network: "udp", Addr: "127.0.0.1:0"}},
		}
		if c.packetOK {
			datagram.HandleFunc(noopPacketHandler)
		}

		err := srv.bindEverything(t.Context(), []*StreamGroup{stream}, []*PacketGroup{datagram})

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("bindEverything = %v, want code %v", err, c.wantCode)
			}
			//: a stream failure aborts before any datagram socket is bound.
			if !c.streamOK {
				srv.mu.RLock()
				conns := len(srv.packetConns)
				srv.mu.RUnlock()
				if conns != 0 {
					t.Errorf("%d datagram sockets were bound after the stream side failed", conns)
				}
			}
			return
		}
		if err != nil {
			t.Fatalf("bindEverything = %v, want nil", err)
		}
		srv.mu.RLock()
		listeners := len(srv.listeners)
		conns := len(srv.packetConns)
		srv.mu.RUnlock()
		if listeners == 0 || conns == 0 {
			t.Fatalf("bound %d listeners and %d datagram sockets, want both", listeners, conns)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_adoptStreamSockets pins that an inherited listener is served on
// exactly the same terms as a bound one — adoption changes where the socket came
// from, not how it is served — and that State says which it was.
//
// The distinction matters to an operator: an adopted socket survived a restart,
// a freshly bound one did not.
func Test_Server_adoptStreamSockets(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// published is what the supervisor passed, by name.
		published []string
		// requested are the names the group asks for.
		requested []string
		// wantCode is the refusal, or zero when every name is adopted.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "nothing to adopt"},
		{name: "one published socket", published: []string{"api"}, requested: []string{"api"}},
		{
			name:      "two published sockets",
			published: []string{"api", "admin"}, requested: []string{"api", "admin"},
		},
		{
			//: a name the supervisor did not pass is a configuration mismatch
			//: between the unit file and the program, never a reason to bind.
			name:      "a name nobody published",
			published: []string{"api"}, requested: []string{"admin"},
			wantCode: corenet.CodeSocketAdoptFailed,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		table := make(map[string][]*os.File, len(c.published))
		for _, name := range c.published {
			file, _ := adoptableListenerFile(t)
			releaseOnCleanup(t, file)
			table[name] = []*os.File{file}
		}
		srv := newTestServer(t)
		srv.inheritOnce.Do(func() { srv.inherited = table })
		group := &StreamGroup{name: "api", adopt: c.requested}
		group.HandleFunc(noopConn)

		err := srv.adoptStreamSockets(group, group.resolved())

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("adoptStreamSockets = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("adoptStreamSockets = %v, want nil", err)
		}
		srv.mu.RLock()
		listeners := len(srv.listeners)
		rows := slices.Clone(srv.states)
		srv.mu.RUnlock()
		if listeners != len(c.requested) {
			t.Fatalf("adopted %d listeners, want %d", listeners, len(c.requested))
		}
		for i, row := range rows {
			//: an operator reading State has to be able to tell a socket that
			//: survived a restart from one that did not.
			if !row.Adopted {
				t.Errorf("row %d does not report the listener as adopted", i)
			}
			if row.Shards != 1 {
				t.Errorf("row %d reports %d shards, want 1", i, row.Shards)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_adoptPacketSockets is the datagram mirror. sdlisten only wraps
// stream sockets, so this half goes through the raw descriptors and needs its
// own coverage rather than inheriting the stream half's.
func Test_Server_adoptPacketSockets(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// published is what the supervisor passed, by name.
		published []string
		// requested are the names the group asks for.
		requested []string
		// wantCode is the refusal, or zero when every name is adopted.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "nothing to adopt"},
		{name: "one published socket", published: []string{"dns"}, requested: []string{"dns"}},
		{
			name:      "a name nobody published",
			published: []string{"dns"}, requested: []string{"sip"},
			wantCode: corenet.CodeSocketAdoptFailed,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		table := make(map[string][]*os.File, len(c.published))
		for _, name := range c.published {
			file, _ := adoptablePacketFile(t)
			releaseOnCleanup(t, file)
			table[name] = []*os.File{file}
		}
		srv := newTestServer(t)
		srv.inheritOnce.Do(func() { srv.inherited = table })
		group := &PacketGroup{name: "dns", adopt: c.requested}
		group.HandleFunc(noopPacketHandler)

		err := srv.adoptPacketSockets(group, group.resolved())

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("adoptPacketSockets = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("adoptPacketSockets = %v, want nil", err)
		}
		srv.mu.RLock()
		conns := len(srv.packetConns)
		rows := slices.Clone(srv.states)
		srv.mu.RUnlock()
		if conns != len(c.requested) {
			t.Fatalf("adopted %d sockets, want %d", conns, len(c.requested))
		}
		for i, row := range rows {
			if !row.Adopted {
				t.Errorf("row %d does not report the socket as adopted", i)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_acceptLoop pins the loop's two ends: it serves what arrives, and
// it STOPS when its listener closes.
//
// Goroutine lifecycle: one goroutine per case runs the loop under test. The case
// ends it by closing the listener and waits for it to publish its exit, so none
// outlives its case.
//
// Closing the listener is the only signal that reliably interrupts a blocking
// Accept, so a loop that treated the resulting error as transient would spin on
// a dead listener forever, holding the in-flight token the drain waits on.
func Test_Server_acceptLoop(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// dials is how many connections are made before the listener closes.
		dials int
	}
	tests := []tc{
		{name: "no traffic at all"},
		{name: "one connection", dials: 1},
		{name: "several connections", dials: 5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		srv := newTestServer(t)
		group := &StreamGroup{name: "api"}
		group.HandleFunc(noopConn)
		bound := &boundListener{group: group.name, ln: ln}

		stopped := make(chan struct{})
		srv.inFlight.Add(1)
		go func() {
			defer close(stopped)
			srv.acceptLoop(bound, group, group.resolved())
		}()

		for i := range c.dials {
			conn, derr := stdnet.DialTimeout("tcp", ln.Addr().String(), 3*time.Second)
			if derr != nil {
				t.Fatalf("dial %d: %v", i, derr)
			}
			if cerr := conn.Close(); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		}
		//: every accepted connection is counted, which is what an operator reads
		//: to tell a busy server from a stuck one.
		deadline := time.Now().Add(5 * time.Second)
		for srv.State().TotalConns < uint64(c.dials) && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		if got := srv.State().TotalConns; got != uint64(c.dials) {
			t.Fatalf("TotalConns = %d, want %d", got, c.dials)
		}

		//: closing the listener is the only signal that ends the loop.
		if cerr := ln.Close(); cerr != nil {
			t.Fatalf("close listener: %v", cerr)
		}

		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatal("the accept loop is still running after its listener closed — " +
				"it would hold the in-flight token the drain waits on forever")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_serve pins that a connection is ALWAYS reclaimed.
//
// Goroutine lifecycle: one goroutine per case waits on the in-flight group so
// the assertion cannot block the test; it closes a channel the case receives
// from within a bounded time either way.
//
// Closing here rather than in the handler means a handler that returns early —
// or panics — still cannot leak a descriptor, a pooled wrapper, an active count
// or an in-flight token. Every one of those is a slow leak that only shows up
// under load, which is exactly when it cannot be diagnosed.
func Test_Server_serve(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// handlerErr is what the handler reports.
		handlerErr error
		// panics makes the handler panic instead of returning.
		panics bool
		// maxConns is the group's ceiling; zero means none.
		maxConns int
	}
	tests := []tc{
		{name: "a handler that succeeds"},
		{name: "a handler that fails", handlerErr: corenet.ServerClosed},
		//: a panicking handler closes its own connection and nothing more.
		{name: "a handler that panics", panics: true},
		{name: "a handler under a ceiling", maxConns: 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		group := &StreamGroup{name: "api", limits: corenet.LimitsValue{MaxConns: c.maxConns}}
		group.HandleFunc(func(context.Context, corenet.Conn) error {
			if c.panics {
				panic("handler exploded")
			}
			return c.handlerErr
		})
		group.limiter = newConnLimiter(group.limits)
		socket := &fakeSocket{}
		srv.active.Add(1)
		srv.inFlight.Add(1)

		srv.serve(socket, group, group.resolved())

		//: the socket is closed whatever the handler did.
		if socket.closes == 0 {
			t.Error("the connection was not closed — a handler that panics would leak it")
		}
		//: and the accounting is unwound, or the drain would wait on a
		//: connection that finished.
		if active := srv.State().ActiveConns; active != 0 {
			t.Errorf("ActiveConns = %d after the handler returned, want 0", active)
		}
		waited := make(chan struct{})
		go func() {
			srv.inFlight.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(3 * time.Second):
			t.Fatal("serve returned without releasing its in-flight token — the " +
				"drain would never complete")
		}
		//: the wrapper went back to the pool with nothing left on it.
		srv.liveMu.RLock()
		live := len(srv.live)
		srv.liveMu.RUnlock()
		if live != 0 {
			t.Errorf("%d sockets are still registered as live", live)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_waitIdle pins that the drain has TEETH.
//
// Goroutine lifecycle: the cases whose connections finish start one goroutine
// that releases them after a moment; it ends on its own and touches only an
// atomic counter.
//
// A budget that was waited out rather than enforced is not a budget: a handler
// blocked on something other than its socket cannot be killed, so the wait has
// to end and report instead of blocking forever. That report is what lets
// Shutdown sever the remaining sockets rather than hang.
func Test_Server_waitIdle(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// active is how many connections are in flight.
		active int64
		// budget is how long the drain is given.
		budget time.Duration
		// finishes releases the connections after a moment.
		finishes bool
	}
	tests := []tc{
		{name: "nothing in flight", budget: 5 * time.Second},
		{name: "connections that finish", active: 3, budget: 5 * time.Second, finishes: true},
		//: a handler that never returns must not make the drain block forever.
		{name: "a connection that never finishes", active: 1, budget: 100 * time.Millisecond},
		{name: "several that never finish", active: 8, budget: 100 * time.Millisecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		srv.active.Add(c.active)
		if c.finishes {
			go func() {
				time.Sleep(20 * time.Millisecond)
				srv.active.Add(-c.active)
			}()
		}
		ctx, cancel := context.WithTimeout(t.Context(), c.budget)
		defer cancel()

		err := srv.waitIdle(ctx)

		if c.active == 0 || c.finishes {
			if err != nil {
				t.Fatalf("waitIdle = %v, want a clean drain", err)
			}
			return
		}
		//: the overrun is reported rather than waited out.
		if !errs.HasCode(err, corenet.CodeDrainTimeout) {
			t.Fatalf("waitIdle = %v, want DRAIN_TIMEOUT", err)
		}
		//: the report names what was still running, which is what an operator
		//: needs to know whether to raise the budget or fix the handler.
		var named bool
		for _, f := range errs.FieldsOf(err) {
			if f.Key() == "active" {
				named = true
			}
		}
		if !named {
			t.Errorf("the overrun does not say what was still in flight: %v", errs.FieldsOf(err))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_closeListeners pins that closing FORGETS the listeners as well as
// closing them.
//
// Shutdown and Close can both run, in either order, and a listener closed twice
// would report an error the caller has no use for. Taking the slice under the
// lock and clearing it is what makes the second call a no-op instead.
func Test_Server_closeListeners(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// listeners and packets are how many of each are bound.
		listeners int
		packets   int
	}
	tests := []tc{
		{name: "nothing bound"},
		{name: "one listener", listeners: 1},
		{name: "one datagram socket", packets: 1},
		{name: "a mixed server", listeners: 2, packets: 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		addrs := make([]string, 0, c.listeners)
		packetAddrs := make([]string, 0, c.packets)
		for range c.listeners {
			ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			srv.listeners = append(srv.listeners, &boundListener{ln: ln})
			addrs = append(addrs, ln.Addr().String())
		}
		for range c.packets {
			pc, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen packet: %v", err)
			}
			srv.packetConns = append(srv.packetConns, &boundPacketConn{pc: pc})
			packetAddrs = append(packetAddrs, pc.LocalAddr().String())
		}

		srv.closeListeners()

		//: the ports are actually released, which is what makes a restart on
		//: the same address possible.
		for _, addr := range addrs {
			assertReleased(t, "tcp", addr)
		}
		for _, addr := range packetAddrs {
			assertReleased(t, "udp", addr)
		}
		//: forgotten as well as closed, so a second call cannot double-close.
		srv.mu.RLock()
		remaining := len(srv.listeners) + len(srv.packetConns)
		srv.mu.RUnlock()
		if remaining != 0 {
			t.Fatalf("%d listeners are still registered after being closed", remaining)
		}
		//: Shutdown and Close can both run, in either order.
		srv.closeListeners()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_shutdownHTTP pins that an HTTP group's embedded server is stopped
// GRACEFULLY, and that a plain ConnHandler group costs nothing.
//
// Stopping it first is what releases net/http's keep-alive connections: without
// it they hold the drain open to its full budget on every shutdown of an idle
// HTTP server, which looks exactly like a stuck handler.
func Test_Server_shutdownHTTP(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// httpGroups is how many groups serve an http.Handler.
		httpGroups int
		// plainGroups is how many serve a plain ConnHandler.
		plainGroups int
	}
	tests := []tc{
		{name: "no groups at all"},
		{name: "only plain groups", plainGroups: 2},
		{name: "one HTTP group", httpGroups: 1},
		{name: "a mixed server", httpGroups: 2, plainGroups: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		adapters := make([]*httpAdapter, 0, c.httpGroups)
		//: distinct names, or the second declaration is a duplicate and hands
		//: back a DETACHED group the server never sees.
		for i := range c.httpGroups {
			group := srv.Group("http" + strconv.Itoa(i))
			group.HandleHTTP(http.NotFoundHandler())
			adapters = append(adapters, group.httpAdapter)
		}
		for i := range c.plainGroups {
			srv.Group("plain" + strconv.Itoa(i)).HandleFunc(noopConn)
		}

		srv.shutdownHTTP(t.Context())

		//: an adapter that never saw a connection has nothing to stop, but it
		//: must still be marked so a late connection cannot start a server
		//: nothing owns.
		for i, adapter := range adapters {
			adapter.mu.RLock()
			stopped := adapter.stopped
			adapter.mu.RUnlock()
			if !stopped {
				t.Errorf("adapter %d was not marked stopped — a connection arriving "+
					"now would start a goroutine the engine does not track", i)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_closeHTTP is Close's counterpart to shutdownHTTP.
//
// Close severs live sockets rather than waiting for them, so the embedded server
// is closed the same way instead of being left to finish. Without it, Close
// ended every listener the engine knew about and left the http.Server running on
// its bridge — a goroutine, an http.Server and its keep-alive machinery
// outliving the server that owned them, on every Close of an HTTP group.
func Test_Server_closeHTTP(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// httpGroups is how many groups serve an http.Handler.
		httpGroups int
		// plainGroups is how many serve a plain ConnHandler.
		plainGroups int
	}
	tests := []tc{
		{name: "no groups at all"},
		{name: "only plain groups", plainGroups: 2},
		{name: "one HTTP group", httpGroups: 1},
		{name: "a mixed server", httpGroups: 2, plainGroups: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := newTestServer(t)
		adapters := make([]*httpAdapter, 0, c.httpGroups)
		//: distinct names, or the second declaration is a duplicate and hands
		//: back a DETACHED group the server never sees.
		for i := range c.httpGroups {
			group := srv.Group("http" + strconv.Itoa(i))
			group.HandleHTTP(http.NotFoundHandler())
			adapters = append(adapters, group.httpAdapter)
		}
		for i := range c.plainGroups {
			srv.Group("plain" + strconv.Itoa(i)).HandleFunc(noopConn)
		}

		srv.closeHTTP()

		for i, adapter := range adapters {
			adapter.mu.RLock()
			stopped := adapter.stopped
			adapter.mu.RUnlock()
			if !stopped {
				t.Errorf("adapter %d survived Close — its goroutine would outlive "+
					"the server that owned it", i)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
