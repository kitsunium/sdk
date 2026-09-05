// Package server_test — the engine as a caller wires it.
package server_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

const (
	// pollInterval is how often waitFor re-checks its condition. Polling beats a
	// fixed sleep: it is both faster in the common case and far less flaky.
	pollInterval time.Duration = 2 * time.Millisecond
	// serveDeadline bounds a wait for work that is already in flight — a
	// connection dialled, a datagram sent. It is deliberately far longer than
	// the work takes: the assertion is that the engine serves it AT ALL, not
	// that it serves it quickly, so the deadline exists to fail instead of
	// hanging. A tight one measures the machine's load instead, which is how a
	// suite acquires a test that only fails under -race on a busy runner.
	serveDeadline time.Duration = 30 * time.Second
)

// startEcho starts an echo server on an ephemeral port and returns it with the
// address it actually bound.
func startEcho(t *testing.T, opts ...server.GroupOption) (srv *server.Server, addr string) {
	t.Helper()
	srv = server.New()
	all := append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0")}, opts...)
	srv.Group("echo", all...).HandleFunc(echoHandler)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	state := srv.State()
	if len(state.Listeners) != 1 {
		t.Fatalf("listeners = %d, want 1", len(state.Listeners))
	}
	return srv, state.Listeners[0].Address
}

// roundTrip sends one line and reads the echoed reply.
func roundTrip(t *testing.T, addr, line string) string {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if cerr := c.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if _, werr := io.WriteString(c, line+"\n"); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	got, rerr := bufio.NewReader(c).ReadString('\n')
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return got
}

// readLine reads one line and asserts it.
func readLine(t *testing.T, c stdnet.Conn, want string) {
	t.Helper()
	if derr := c.SetReadDeadline(time.Now().Add(3 * time.Second)); derr != nil {
		t.Fatalf("set deadline: %v", derr)
	}
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != want {
		t.Fatalf("read %q, want %q", buf, want)
	}
}

// sendDatagram sends one datagram and returns the reply.
func sendDatagram(t *testing.T, addr, payload string) string {
	t.Helper()
	c, err := stdnet.DialTimeout("udp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeOrFail(t, c)
	if _, werr := c.Write([]byte(payload)); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	if derr := c.SetReadDeadline(time.Now().Add(3 * time.Second)); derr != nil {
		t.Fatalf("set deadline: %v", derr)
	}
	buf := make([]byte, 2048)
	n, rerr := c.Read(buf)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return string(buf[:n])
}

// startUpper starts a datagram server that echoes each payload back uppercased,
// so a reply proves the handler actually saw the bytes.
func startUpper(t *testing.T, opts ...server.GroupOption) (srv *server.Server, addr string) {
	t.Helper()
	srv = server.New()
	all := append([]server.GroupOption{server.Listen("udp", "127.0.0.1:0")}, opts...)
	srv.PacketGroup("dns", all...).
		HandleFunc(func(_ context.Context, p corenet.Packet) error {
			out := make([]byte, len(p.Data()))
			for i, b := range p.Data() {
				out[i] = upper(b)
			}
			_, err := p.Reply(out)
			return err
		})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	return srv, srv.State().Listeners[0].Address
}

// upper uppercases one ASCII byte.
func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 'a' + 'A'
	}
	return b
}

// echoHandler copies whatever it receives straight back.
func echoHandler(_ context.Context, c corenet.Conn) error {
	_, err := io.Copy(c, c)
	return err
}

// noopHandler is a handler that does nothing, for declaration-error tests.
func noopHandler(context.Context, corenet.Conn) error {
	return nil
}

// noopPacket is a datagram handler that does nothing.
func noopPacket(context.Context, corenet.Packet) error {
	return nil
}

// waitFor polls cond until it holds or the deadline passes, and reports whether
// it ever held. Polling beats a fixed sleep: it is both faster in the common
// case and far less flaky.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(pollInterval)
	}
	return false
}

// closeOrFail closes c and reports a close error rather than discarding it: a
// failing close on a server socket usually means the connection was already
// broken, which is exactly what a test needs to know.
func closeOrFail(t *testing.T, c io.Closer) {
	t.Helper()
	//: a close failure is reported, never swallowed.
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// listenTCP binds an ephemeral TCP listener for a test.
func listenTCP(t *testing.T) *stdnet.TCPListener {
	t.Helper()
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcp, ok := ln.(*stdnet.TCPListener)
	if !ok {
		t.Fatalf("expected a *net.TCPListener, got %T", ln)
	}
	return tcp
}

// selfSignedIdentity builds a server TLS identity for the handshake tests.
func selfSignedIdentity(t *testing.T) corenet.IdentityValue {
	t.Helper()
	key, kerr := ecdsa.GenerateKey(elliptic.P256(), nil)
	if kerr != nil {
		t.Fatalf("generate key: %v", kerr)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kitsunium-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, cerr := x509.CreateCertificate(nil, tmpl, tmpl, &key.PublicKey, key)
	if cerr != nil {
		t.Fatalf("create certificate: %v", cerr)
	}
	keyDER, merr := x509.MarshalECPrivateKey(key)
	if merr != nil {
		t.Fatalf("marshal key: %v", merr)
	}
	id, ierr := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:     pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		MinVersion: tls.VersionTLS12,
	})
	if ierr != nil {
		t.Fatalf("identity: %v", ierr)
	}
	return id
}

// TestNew pins that a freshly built server is USABLE and has bound nothing.
//
// Options apply in declaration order, so a later one deliberately wins — which
// is what lets a caller layer a base configuration and then override a piece of
// it without the two fighting.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// drains are the drain budgets applied, in order.
		drains []time.Duration
	}
	tests := []tc{
		{name: "no options at all"},
		{name: "a drain budget", drains: []time.Duration{time.Second}},
		//: applied in order, so the last one wins.
		{name: "two drain budgets", drains: []time.Duration{time.Second, 5 * time.Second}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		opts := make([]server.Option, 0, len(c.drains))
		for _, d := range c.drains {
			opts = append(opts, server.WithDrainTimeout(d))
		}

		srv := server.New(opts...)

		if srv == nil {
			t.Fatal("New returned no server")
		}
		state := srv.State()
		//: a freshly built server has bound nothing yet, so a caller can still
		//: declare groups on it.
		if state.Phase != corenet.PhaseNew {
			t.Fatalf("phase = %v, want new", state.Phase)
		}
		if len(state.Listeners) != 0 {
			t.Fatalf("a new server already reports %d listeners", len(state.Listeners))
		}
		//: and shutting one down that never started is a no-op, not an error.
		if err := srv.Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown on a new server = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestServer_Group pins the ergonomic trade this API makes.
//
// Group returns the group rather than (group, error) so declaring a server stays
// a single chained expression instead of an error check per line. That is only
// acceptable because the mistake is still REPORTED — at Start — and because a
// refused declaration hands back a detached group, so the caller's chained calls
// stay safe rather than panicking on a nil.
func TestServer_Group(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// names are the group names declared, in order.
		names []string
		// wantCode is what Start must report, or zero when the wiring is sound.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "one group", names: []string{"api"}},
		{name: "two distinct groups", names: []string{"api", "admin"}},
		//: a duplicate name would make logs, metrics and State ambiguous.
		{name: "a duplicate name", names: []string{"api", "api"}, wantCode: corenet.CodeGroupDuplicate},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		for _, name := range c.names {
			group := srv.Group(name, server.Listen("tcp", "127.0.0.1:0"))
			//: even a refused declaration hands back a usable value, so the
			//: chained call below cannot panic.
			if group == nil {
				t.Fatalf("Group(%q) returned nothing to chain on", name)
			}
			if group.Name() != name {
				t.Errorf("Group(%q).Name() = %q", name, group.Name())
			}
			group.HandleFunc(noopHandler)
		}

		err := srv.Start(t.Context())

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("Start = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("Start = %v, want nil", err)
		}
		if got := len(srv.State().Listeners); got != len(c.names) {
			t.Fatalf("State reports %d listeners for %d groups", got, len(c.names))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestServer_PacketGroup mirrors Group deliberately: the whole point of the
// domain is that a UDP service is declared the same way a TCP one is.
//
// Names are shared across both natures, so a datagram group cannot take a name a
// stream group already holds — otherwise one would shadow the other in logs,
// metrics and State, where nothing records which nature a row belongs to.
func TestServer_PacketGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// streamNames are the stream groups declared first.
		streamNames []string
		// packetNames are the datagram groups declared after them.
		packetNames []string
		// wantCode is what Start must report, or zero when the wiring is sound.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "one datagram group", packetNames: []string{"dns"}},
		{name: "two distinct datagram groups", packetNames: []string{"dns", "sip"}},
		{name: "a mixed server", streamNames: []string{"api"}, packetNames: []string{"dns"}},
		{name: "a duplicate datagram name", packetNames: []string{"dns", "dns"}, wantCode: corenet.CodeGroupDuplicate},
		{
			//: one name, two natures: ambiguous everywhere it is reported.
			name:        "a name a stream group already holds",
			streamNames: []string{"shared"}, packetNames: []string{"shared"},
			wantCode: corenet.CodeGroupDuplicate,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		for _, name := range c.streamNames {
			srv.Group(name, server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopHandler)
		}
		for _, name := range c.packetNames {
			group := srv.PacketGroup(name, server.Listen("udp", "127.0.0.1:0"))
			if group == nil {
				t.Fatalf("PacketGroup(%q) returned nothing to chain on", name)
			}
			if group.Name() != name {
				t.Errorf("PacketGroup(%q).Name() = %q", name, group.Name())
			}
			group.HandleFunc(noopPacket)
		}

		err := srv.Start(t.Context())

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("Start = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("Start = %v, want nil", err)
		}
		want := len(c.streamNames) + len(c.packetNames)
		if got := len(srv.State().Listeners); got != want {
			t.Fatalf("State reports %d listeners, want %d", got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestServer_State pins that the snapshot is a COPY and that it reports the
// address the kernel actually chose.
//
// A caller cannot dial what it asked for; it has to dial what it got, so ":0"
// would make State useless for the one thing it exists for. And the listener
// slice is cloned so a caller cannot observe the set mutating underneath it —
// nor mutate ours.
func TestServer_State(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// requests is how many connections are made before the snapshot.
		requests int
	}
	tests := []tc{
		{name: "an idle server"},
		{name: "after one connection", requests: 1},
		{name: "after several connections", requests: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, addr := startEcho(t)
		for range c.requests {
			roundTrip(t, addr, "x")
		}

		state := srv.State()

		if state.Phase != corenet.PhaseServing {
			t.Fatalf("phase = %v, want serving", state.Phase)
		}
		//: the kernel-assigned port, not the ":0" that was requested.
		if state.Listeners[0].Address != addr || addr == "127.0.0.1:0" {
			t.Fatalf("address = %q, want the kernel-assigned port", state.Listeners[0].Address)
		}
		if state.Degraded() {
			t.Fatal("a plain TCP listener reported degradation")
		}
		//: the counters are what an operator reads to tell a busy server from a
		//: stuck one, so they have to move.
		if !waitFor(t, func() bool { return srv.State().TotalConns == uint64(c.requests) }) {
			t.Fatalf("TotalConns = %d after %d connections", srv.State().TotalConns, c.requests)
		}
		if !waitFor(t, func() bool { return srv.State().ActiveConns == 0 }) {
			t.Fatalf("ActiveConns = %d with nothing in flight", srv.State().ActiveConns)
		}
		//: a copy, so a caller mutating what it was handed cannot corrupt the
		//: server's own view.
		state.Listeners[0].Address = "tampered"
		if srv.State().Listeners[0].Address != addr {
			t.Fatal("State handed out the server's own listener slice")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
