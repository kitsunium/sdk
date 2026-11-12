// Package server_test — HTTP/2's bounds as a group option (#965): the zero
// value reads back as ADR 0195's numbers, an out-of-range bound fails Start
// naming it, the other groups refuse it, and a stream window raised by the
// option lets an upload past what the default window allows at a round trip.
package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	stdnet "net"
	"net/http"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// delayConn delays every read by delay: a round trip of latency on the client's
// side, where a WINDOW_UPDATE comes in, so the send window's refill waits for it.
type delayConn struct {
	stdnet.Conn
	delay  time.Duration
	once   sync.Once
	chunks chan delayChunk
	rest   []byte
	err    error
}

type delayChunk struct {
	data []byte
	at   time.Time
	err  error
}

func newDelayConn(c stdnet.Conn, delay time.Duration) *delayConn {
	return &delayConn{Conn: c, delay: delay, chunks: make(chan delayChunk, 4096)}
}

func (d *delayConn) pump() {
	buf := make([]byte, 64<<10)
	for {
		n, err := d.Conn.Read(buf)
		d.chunks <- delayChunk{data: bytes.Clone(buf[:n]), at: time.Now().Add(d.delay), err: err}
		if err != nil {
			close(d.chunks)
			return
		}
	}
}

func (d *delayConn) Read(p []byte) (int, error) {
	d.once.Do(func() { go d.pump() })
	for len(d.rest) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		ch, ok := <-d.chunks
		if !ok {
			return 0, io.EOF
		}
		time.Sleep(time.Until(ch.at))
		d.rest, d.err = ch.data, ch.err
	}
	n := copy(p, d.rest)
	d.rest = d.rest[n:]
	return n, nil
}

// h2Client returns a TLS HTTP/2 client over connections delayed by delay.
func h2Client(t *testing.T, pool *x509.CertPool, delay time.Duration) *http.Client {
	t.Helper()
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "kitsunium-test", NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12}}
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (stdnet.Conn, error) {
		raw, err := (&stdnet.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		c := tls.Client(newDelayConn(raw, delay), tr.TLSClientConfig.Clone())
		if err := c.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return c, nil
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 2 * time.Minute}
}

// startH2Group starts a TLS HTTP group on a loopback port, echoing the body's
// length; it counts as drained by the test.
func startH2Group(t *testing.T, id corenet.IdentityValue, opts ...server.GroupOption) (*server.Server, string) {
	t.Helper()
	srv := server.New()
	t.Cleanup(func() { _ = srv.Close() })
	all := append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0"), server.TLS(id), server.MaxBodyBytes(1 << 30)}, opts...)
	srv.Group("web", all...).HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, http.StatusText(http.StatusOK))
	}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return srv, srv.State().Listeners[0].Address
}

// timeUpload posts size bytes and returns how long it took.
func timeUpload(t *testing.T, cli *http.Client, addr string, size int) time.Duration {
	t.Helper()
	body := make([]byte, size)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+addr+"/", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %s over HTTP/%d, want 200 on 2", resp.Status, resp.ProtoMajor)
	}
	return time.Since(start)
}

// TestHTTP2Bounds_window is #965's first criterion, at the issue's 100 ms, on 32 MiB rather than
// 64 MiB so a -race run stays short: a stream window raised
// by the option lets the upload past what the default 256 KiB allows.
//
// The ratio is taken on the time the window costs, each upload less the same
// upload with no latency injected (floor): what the host spends moving 32 MiB
// through TLS is the same on both groups and is not the option's. Measured on
// 2026-11-11: on amd64 under -race the floor is 0.7 s and the raw ratio 12x;
// on linux/386 the floor is 1.4 s and the raw ratio fell to 7.7x-7.9x, while
// the window's own share was 0.3 s against 11.9 s.
func TestHTTP2Bounds_window(t *testing.T) {
	const size = 32 << 20
	const delay = 100 * time.Millisecond
	id, pool := alpnIdentity(t, "h2")
	_, defaultAddr := startH2Group(t, id)
	_, wideAddr := startH2Group(t, id, server.HTTP2(corehttp2.LimitsValue{ReceivePerStream: 4 << 20, ReceivePerConn: 16 << 20}))
	base := timeUpload(t, h2Client(t, pool, delay), defaultAddr, size)
	wide := timeUpload(t, h2Client(t, pool, delay), wideAddr, size)
	floor := timeUpload(t, h2Client(t, pool, 0), wideAddr, size)
	t.Logf("%d MiB at %v: default %v, raised window %v, no latency %v", size>>20, delay, base, wide, floor)
	if wide-floor > (base-floor)/8 {
		t.Fatalf("upload took %v with the raised window against %v by default (%v with no latency), want the latency's share at least 8x smaller", wide, base, floor)
	}
}

// TestHTTP2Bounds_state pins that State reports the effective bounds, zero
// reading back as ADR 0195 §D3's numbers.
func TestHTTP2Bounds_state(t *testing.T) {
	id, _ := alpnIdentity(t, "h2")
	srv, _ := startH2Group(t, id)
	row := srv.State().Listeners[0]
	if row.HTTP2 == nil {
		t.Fatal("no bounds on an HTTP group's row")
	}
	want := corehttp2.LimitsValue{MaxStreams: 100, ReceivePerStream: 256 << 10, ReceivePerConn: 1 << 20, MaxFrame: 16 << 10, HeaderTable: 4 << 10, MaxHeaderList: 8 << 10}
	if *row.HTTP2 != want {
		t.Fatalf("bounds %+v, want %+v", *row.HTTP2, want)
	}
}

// TestHTTP2Bounds_refusals pins the option's refusals at Start.
func TestHTTP2Bounds_refusals(t *testing.T) {
	id, _ := alpnIdentity(t, "h2")
	h1h := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("x") })
	t.Run("max_frame", func(t *testing.T) {
		srv := server.New()
		srv.Group("web", server.Listen("tcp", "127.0.0.1:0"), server.TLS(id), server.HTTP2(corehttp2.LimitsValue{MaxFrame: 8 << 10})).
			HandleHTTP(http.NotFoundHandler())
		err := srv.Start(t.Context())
		t.Cleanup(func() { _ = srv.Close() })
		if !errs.HasCode(err, corehttp2.CodeLimitsRefused) || !fieldIs(err, "reason", "max_frame") {
			t.Fatalf("Start = %v, want LimitsRefused reason=max_frame", err)
		}
	})
	t.Run("http1 loop group", func(t *testing.T) {
		srv := server.New()
		srv.Group("web", server.Listen("tcp", "127.0.0.1:0"), server.Plaintext(), server.HTTP2(corehttp2.LimitsValue{})).
			HandleLoop(http1.HandleLoop(h1h, http1.Parser{}))
		err := srv.Start(t.Context())
		t.Cleanup(func() { _ = srv.Close() })
		if !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) || !fieldIs(err, "option", "HTTP2") {
			t.Fatalf("Start = %v, want LoopOptionUnsupported option=HTTP2", err)
		}
	})
	t.Run("datagram group", func(t *testing.T) {
		srv := server.New()
		srv.PacketGroup("udp", server.Listen("udp", "127.0.0.1:0"), server.HTTP2(corehttp2.LimitsValue{})).
			HandleFunc(noopPacket)
		err := srv.Start(t.Context())
		t.Cleanup(func() { _ = srv.Close() })
		if !errs.HasCode(err, corenet.CodePacketOptionUnsupported) || !fieldIs(err, "option", "HTTP2") {
			t.Fatalf("Start = %v, want PacketOptionUnsupported option=HTTP2", err)
		}
	})
}
