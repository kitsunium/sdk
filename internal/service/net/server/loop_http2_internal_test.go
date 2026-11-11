// Package server — HTTP/2 on the loop engine (ADR 0220, #961): one group
// serving a Go client over h2c and a raw HTTP/1.1 client through one chain,
// Pending answers, the operator's switch, Protocols, the drain, the idle
// memory and the state machine's defences, on every backend of the platform.
package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	stdnet "net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// h2cClient returns an http.Client speaking h2c by prior knowledge to addr.
func h2cClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{DisableKeepAlives: false}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetUnencryptedHTTP2(true)
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}

// TestLoopHTTP2_oneChainBothVersions is the conformance of #961's first
// criterion: N connections × N tokens, half over h2c by a Go client, half over
// raw HTTP/1.1, through one Routes+Bearer chain on two lanes on every
// backend, under -race, with released buffers marked; the subject of each
// token survives every buffer reuse.
func TestLoopHTTP2_oneChainBothVersions(t *testing.T) {
	const conns, rounds = 12, 20
	markRecycled.Store(true)
	t.Cleanup(func() { markRecycled.Store(false) })
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			log := &authLog{}
			routes, err := http1.Routes(http1.RouteValue{ID: 1, Path: "/whoami"}, http1.RouteValue{ID: 2, Path: "/health"})
			if err != nil {
				t.Fatal(err)
			}
			bearer, err := http1.Bearer(conformanceVerifier{}, 2)
			if err != nil {
				t.Fatal(err)
			}
			app := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
				if req.Route != 1 {
					res.Status = http.StatusNoContent
					return
				}
				tok := string(req.Authorization[len("Bearer "):])
				log.mu.Lock()
				log.recs = append(log.recs, authRec{token: tok, auth: req.Auth, lane: req.Lane})
				log.mu.Unlock()
				res.Body = append([]byte(nil), req.Auth.Subject()...)
			})
			h := http2.HandleLoop(corenet.Chain[corenet.HTTP1Handler](app, routes, bearer), http1.Parser{})
			_, addr := startLoopServer(t, b, h, Loop(corenet.LoopValue{Backend: b, Loops: 2}), Plaintext(), Protocols("h2", "http/1.1"))
			var wg sync.WaitGroup
			errc := make(chan error, 2*conns)
			for i := range conns {
				wg.Go(func() { errc <- tokenRounds(t, loopDial, addr, i, rounds) })
				wg.Go(func() { errc <- h2TokenRounds(t, addr, conns+i, rounds) })
			}
			wg.Wait()
			close(errc)
			for err := range errc {
				if err != nil {
					t.Fatal(err)
				}
			}
			log.mu.Lock()
			defer log.mu.Unlock()
			if len(log.recs) != 2*conns*rounds {
				t.Fatalf("%d requests served, want %d", len(log.recs), 2*conns*rounds)
			}
			for _, r := range log.recs {
				i, _ := tokenIndex([]byte(r.token))
				if want := "user-" + strconv.Itoa(i); string(r.auth.Subject()) != want || r.auth.Claims() != uint64(i) {
					t.Fatalf("token %s kept subject %q claims %d, want %q", r.token, r.auth.Subject(), r.auth.Claims(), want)
				}
			}
		})
	}
}

// h2TokenRounds is one Go client's half of the conformance: rounds of
// /whoami with token i, each followed by a request with no token that the
// chain must refuse 401.
func h2TokenRounds(t *testing.T, addr string, i, rounds int) error {
	t.Helper()
	cli := h2cClient(t)
	for range rounds {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/whoami", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer tok-"+strconv.Itoa(i))
		resp, err := cli.Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		swallowErr(resp.Body.Close())
		if err != nil {
			return err
		}
		if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusOK || string(body) != "user-"+strconv.Itoa(i) {
			return errTest("whoami: proto " + strconv.Itoa(resp.ProtoMajor) + " status " + resp.Status + " body " + string(body))
		}
		req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/whoami", nil)
		if err != nil {
			return err
		}
		resp, err = cli.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		swallowErr(resp.Body.Close())
		if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusUnauthorized {
			return errTest("no token: proto " + strconv.Itoa(resp.ProtoMajor) + " status " + resp.Status)
		}
	}
	return nil
}

// errTest is a plain test failure value, the tests' errors being compared to
// nothing.
type errTest string

func (e errTest) Error() string { return string(e) }

// worker is an HTTP1Worker test handler: a target ending in /slow is
// pending, answered by the worker when released; anything else is answered at
// once with its target.
type workerH struct {
	// release is closed to let the pending answers go.
	release chan struct{}
	// worked counts the jobs the worker ran.
	worked atomic.Int64
	// fresh names each pending request by job.
	mu   sync.Mutex
	next uint64
	jobs map[uint64]string
}

func newWorkerH() *workerH {
	return &workerH{release: make(chan struct{}), jobs: map[uint64]string{}}
}

func (h *workerH) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	path := string(req.Path)
	if strings.HasSuffix(path, "/slow") {
		h.mu.Lock()
		h.next++
		id := h.next
		h.jobs[id] = path
		h.mu.Unlock()
		res.Pending, res.Job = true, id
		return
	}
	res.Body = []byte("fast " + path)
}

func (h *workerH) WorkHTTP1(_ int, job uint64, res *corenet.HTTP1ResponseValue) {
	<-h.release
	h.worked.Add(1)
	h.mu.Lock()
	path := h.jobs[job]
	h.mu.Unlock()
	res.Body = []byte("worked " + path)
}

// rawH2 is a test client speaking frames on one TCP connection.
type rawH2 struct {
	t    *testing.T
	conn stdnet.Conn
	br   *bufio.Reader
	mu   sync.Mutex
}

// dialRawH2 connects to addr and sends the preface and an empty SETTINGS.
func dialRawH2(t *testing.T, addr string) *rawH2 {
	t.Helper()
	conn, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(conn.Close()) })
	c := &rawH2{t: t, conn: conn, br: bufio.NewReaderSize(conn, 1<<16)}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	c.send(corehttp2.FrameSettings, 0, 0, nil)
	return c
}

// send writes one frame.
func (c *rawH2) send(typ corehttp2.FrameType, flags corehttp2.FrameFlags, stream uint32, payload []byte) {
	c.t.Helper()
	buf := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: typ, Flags: flags, StreamID: stream, Length: len(payload)})
	buf = append(buf, payload...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.conn.Write(buf); err != nil {
		c.t.Fatal(err)
	}
}

// frameBytes is one frame on the wire.
func frameBytes(typ corehttp2.FrameType, flags corehttp2.FrameFlags, stream uint32, payload []byte) []byte {
	buf := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: typ, Flags: flags, StreamID: stream, Length: len(payload)})
	return append(buf, payload...)
}

// write sends bytes already framed.
func (c *rawH2) write(b []byte) {
	c.t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	//: a flood the server cut short is the point: its answer is what is read.
	if _, err := c.conn.Write(b); err != nil {
		c.t.Logf("write: %v", err)
	}
}

// literalField is an HPACK literal without indexing, both strings plain.
func literalField(name, value string) []byte {
	b := []byte{0x00, byte(len(name))}
	b = append(b, name...)
	b = append(b, byte(len(value)))
	return append(b, value...)
}

// requestBlock encodes a GET of path with extra fields, plainly: every
// representation a literal, so no table is touched.
func requestBlock(method, path string, extra ...string) []byte {
	var b []byte
	b = append(b, literalField(":method", method)...)
	b = append(b, literalField(":scheme", "http")...)
	b = append(b, literalField(":path", path)...)
	b = append(b, literalField(":authority", "h")...)
	for i := 0; i+1 < len(extra); i += 2 {
		b = append(b, literalField(extra[i], extra[i+1])...)
	}
	return b
}

// get sends HEADERS for a GET of path on stream with END_STREAM.
func (c *rawH2) get(stream uint32, path string) {
	c.t.Helper()
	c.send(corehttp2.FrameHeaders, corehttp2.FlagEndStream|corehttp2.FlagEndHeaders, stream, requestBlock("GET", path))
}

// frameEvent is one frame the server sent.
type frameEvent struct {
	h       corehttp2.FrameValue
	payload []byte
}

// next reads the next frame, skipping what the client takes for granted:
// SETTINGS and WINDOW_UPDATE. It fails the test on any read error but EOF,
// which it reports.
func (c *rawH2) next() (frameEvent, error) {
	c.t.Helper()
	for {
		var hdr [corehttp2.FrameHeaderLen]byte
		if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
			return frameEvent{}, err
		}
		h, _, _ := http2.ParseFrameHeader(hdr[:], 1<<20)
		h.Length = int(uint32(hdr[0])<<16 | uint32(hdr[1])<<8 | uint32(hdr[2]))
		h.Type = corehttp2.FrameType(hdr[3])
		h.Flags = corehttp2.FrameFlags(hdr[4])
		h.StreamID = binary.BigEndian.Uint32(hdr[5:]) & 0x7fffffff
		payload := make([]byte, h.Length)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return frameEvent{}, err
		}
		if h.Type == corehttp2.FrameSettings && h.Flags&corehttp2.FlagAck == 0 {
			c.send(corehttp2.FrameSettings, corehttp2.FlagAck, 0, nil)
			continue
		}
		if h.Type == corehttp2.FrameSettings || h.Type == corehttp2.FrameWindowUpdate {
			continue
		}
		return frameEvent{h: h, payload: payload}, nil
	}
}

// goAwayCode reads frames until GOAWAY and returns its code, failing on
// anything else unless a frame it does not care about. EOF before a GOAWAY fails.
func (c *rawH2) untilGoAway() corehttp2.ErrCode {
	c.t.Helper()
	for {
		ev, err := c.next()
		if err != nil {
			c.t.Fatalf("read until GOAWAY: %v", err)
		}
		if ev.h.Type == corehttp2.FrameGoAway {
			return corehttp2.ErrCode(binary.BigEndian.Uint32(ev.payload[4:8]))
		}
	}
}

// TestLoopHTTP2_pendingAndReset pins #961's second criterion: on every backend
// a Pending answer is written by the worker while a second stream of the same
// connection is answered first, and a stream reset before the job returns sees
// no frame for it, its slot being freed by the stale-ref path with nothing
// counted as an error.
func TestLoopHTTP2_pendingAndReset(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			h := newWorkerH()
			srv, addr := startLoopServer(t, b, http2.HandleLoop(h, http1.Parser{}), Plaintext())
			//: auto may start degraded where io_uring is absent (linux/386),
			//: the loop's own fault; nothing else is admitted, and the reset adds none.
			started := startFaults(t, srv, b)
			c := dialRawH2(t, addr)
			c.get(1, "/slow")
			c.get(3, "/fast")
			//: the fast stream answers first.
			got := c.answerOf(3)
			if !strings.Contains(got, "fast /fast") {
				t.Fatalf("stream 3 = %q", got)
			}
			//: reset the pending stream and let the job finish: no frame for it.
			c.send(corehttp2.FrameRSTStream, 0, 1, binary.BigEndian.AppendUint32(nil, uint32(corehttp2.ErrCodeCancel)))
			//: the job may return only once the server has read the reset: a
			//: server that answers before reading it is within RFC 9113 §5.1.
			c.pingSync()
			c.get(5, "/slow2/slow")
			close(h.release)
			got = c.answerOf(5)
			if !strings.Contains(got, "worked /slow2/slow") {
				t.Fatalf("stream 5 = %q", got)
			}
			if st := srv.State(); !slices.Equal(st.Faults, started) {
				t.Fatalf("faults after a reset: %+v, at the start: %+v", st.Faults, started)
			}
		})
	}
}

// startFaults is srv's faults once started on b: none, but on auto the loop's
// own fallback fault where the platform has no io_uring (linux/386), which
// the group reports as degraded from its start.
func startFaults(t *testing.T, srv *Server, b corenet.LoopBackend) []corenet.FaultValue {
	t.Helper()
	st := srv.State()
	for _, f := range st.Faults {
		if b != corenet.LoopBackendAuto || f.Source != faultLoop {
			t.Fatalf("faults at the start on %s: %+v", b, st.Faults)
		}
	}
	return st.Faults
}

// pingSync sends a PING and reads until its ACK: the server reads a
// connection's frames in order, so every frame sent before it has been read.
// A DATA or HEADERS frame on the way fails the test.
func (c *rawH2) pingSync() {
	c.t.Helper()
	opaque := []byte("syncsync")
	c.send(corehttp2.FramePing, 0, 0, opaque)
	for {
		ev, err := c.next()
		if err != nil {
			c.t.Fatalf("waiting for the PING ACK: %v", err)
		}
		if ev.h.Type == corehttp2.FrameData || ev.h.Type == corehttp2.FrameHeaders {
			c.t.Fatalf("a frame for stream %d while waiting for the PING ACK: %+v", ev.h.StreamID, ev.h)
		}
		if ev.h.Type == corehttp2.FramePing && ev.h.Flags&corehttp2.FlagAck != 0 && bytes.Equal(ev.payload, opaque) {
			return
		}
	}
}

// answerOf reads frames until stream's END_STREAM and returns the body, failing
// when a frame for a different stream carries END_STREAM first when strict; here
// other streams' frames are ignored by their own read.
func (c *rawH2) answerOf(stream uint32) string {
	c.t.Helper()
	var body []byte
	for {
		ev, err := c.next()
		if err != nil {
			c.t.Fatalf("stream %d: %v", stream, err)
		}
		if ev.h.StreamID != stream {
			if ev.h.Type == corehttp2.FrameData || ev.h.Type == corehttp2.FrameHeaders {
				c.t.Fatalf("a frame for stream %d while waiting for %d: %+v", ev.h.StreamID, stream, ev.h)
			}
			continue
		}
		if ev.h.Type == corehttp2.FrameData {
			body = append(body, ev.payload...)
		}
		if ev.h.Flags&corehttp2.FlagEndStream != 0 {
			return string(body)
		}
	}
}

// TestLoopHTTP2_protocolsAndSwitch pins #961's third criterion, and the
// Protocols list: h2 alone refuses a non-preface connection with 400 and
// closes, http/1.1 alone refuses the preface the same way, and the operator's
// http2-loop refuses the preface while HTTP/1.1 is served, State naming the
// row disabled.
func TestLoopHTTP2_protocolsAndSwitch(t *testing.T) {
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	for _, b := range loopBackends() {
		t.Run(b.String()+"/h2only", func(t *testing.T) {
			_, addr := startLoopServer(t, b, http2.HandleLoop(hello, http1.Parser{}), Plaintext(), Protocols("h2"))
			assertRefused400(t, addr, "GET / HTTP/1.1\r\nHost: h\r\n\r\n")
			resp := doH2c(t, h2cClient(t), "http://"+addr+"/")
			if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusOK {
				t.Fatalf("h2 on an h2-only group: %d %s", resp.ProtoMajor, resp.Status)
			}
		})
		t.Run(b.String()+"/http1only", func(t *testing.T) {
			_, addr := startLoopServer(t, b, http2.HandleLoop(hello, http1.Parser{}), Plaintext(), Protocols("http/1.1"))
			assertRefused400(t, addr, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
			assertHTTP1OK(t, addr)
		})
		t.Run(b.String()+"/disabled", func(t *testing.T) {
			t.Setenv(corenet.EnvDisable, string(corenet.CapabilityHTTP2Loop))
			srv, addr := startLoopServer(t, b, http2.HandleLoop(hello, http1.Parser{}), Plaintext())
			assertRefused400(t, addr, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
			assertHTTP1OK(t, addr)
			st := srv.State()
			found := false
			for _, lp := range st.Loops {
				for _, c := range lp.Capabilities {
					if c.Name == corenet.CapabilityHTTP2Loop && c.Status == statusDisabled {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("no disabled %s row in %+v", corenet.CapabilityHTTP2Loop, st.Loops)
			}
		})
	}
}

// doH2c performs one GET over h2c and fails the test on a transport error.
func doH2c(t *testing.T, cli *http.Client, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		swallowErr(resp.Body.Close())
	})
	return resp
}

// assertRefused400 sends raw bytes and expects a 400 and a close.
func assertRefused400(t *testing.T, addr, raw string) {
	t.Helper()
	conn, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { swallowErr(conn.Close()) }()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.HasPrefix(all, []byte("HTTP/1.1 400")) {
		t.Fatalf("answer = %q, want a 400 and a close", all)
	}
}

// assertHTTP1OK runs one HTTP/1.1 request over a raw connection.
func assertHTTP1OK(t *testing.T, addr string) {
	t.Helper()
	conn, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { swallowErr(conn.Close()) }()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.HasPrefix(all, []byte("HTTP/1.1 200")) || !bytes.HasSuffix(all, []byte("ok")) {
		t.Fatalf("answer = %q, want a 200 with ok", all)
	}
}

// TestLoopHTTP2_drain pins #961's fourth criterion's first half: on every
// backend the drain sends GOAWAY(NO_ERROR) on an idle h2c connection, an
// in-flight stream is answered, the client observes no reset, and both end
// in a clean close.
func TestLoopHTTP2_drain(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			h := newWorkerH()
			srv, addr := startLoopServer(t, b, http2.HandleLoop(h, http1.Parser{}), Plaintext())
			idle := dialRawH2(t, addr)
			busy := dialRawH2(t, addr)
			busy.get(1, "/slow")
			waitJob(t, h)
			idleDone, busyDone := make(chan corehttp2.ErrCode, 1), make(chan error, 1)
			go func() { idleDone <- idle.untilGoAway() }()
			go func() { busyDone <- busy.drainClean(1, "worked /slow") }()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			shut := make(chan error, 1)
			go func() { shut <- srv.Shutdown(ctx) }()
			time.Sleep(50 * time.Millisecond) // the drain's first sweep has run
			if code := <-idleDone; code != corehttp2.ErrCodeNoError {
				t.Fatalf("idle connection GOAWAY code = %v", code)
			}
			close(h.release)
			if err := <-busyDone; err != nil {
				t.Fatal(err)
			}
			if err := <-shut; err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		})
	}
}

// drainClean reads frames until GOAWAY(NO_ERROR) and stream's body, then EOF,
// and reports anything else, a reset above all.
func (c *rawH2) drainClean(stream uint32, want string) error {
	var body []byte
	sawGoAway, sawEnd := false, false
	for {
		ev, err := c.next()
		if err != nil {
			if err == io.EOF && sawGoAway && sawEnd {
				if !bytes.Contains(body, []byte(want)) {
					return errTest("body = " + string(body))
				}
				return nil
			}
			return errTest("read: " + err.Error() + " goaway=" + strconv.FormatBool(sawGoAway) + " end=" + strconv.FormatBool(sawEnd))
		}
		switch {
		case ev.h.Type == corehttp2.FrameGoAway:
			code := binary.BigEndian.Uint32(ev.payload[4:8])
			if code != uint32(corehttp2.ErrCodeNoError) {
				return errTest("GOAWAY code " + strconv.Itoa(int(code)))
			}
			sawGoAway = true
		case ev.h.Type == corehttp2.FrameRSTStream:
			return errTest("a reset on stream " + strconv.Itoa(int(ev.h.StreamID)))
		case ev.h.StreamID == stream:
			if ev.h.Type == corehttp2.FrameData {
				body = append(body, ev.payload...)
			}
			if ev.h.Flags&corehttp2.FlagEndStream != 0 {
				sawEnd = true
			}
		}
	}
}

// waitJob waits for the pending request to reach the handler.
func waitJob(t *testing.T, h *workerH) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		n := h.next
		h.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the pending request did not arrive")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestLoopHTTP2_defences holds the state machine's defences on real backends:
// a Rapid Reset flood, a CONTINUATION flood and an HPACK bomb each get their
// GOAWAY and end the connection, rather than costing the server.
func TestLoopHTTP2_defences(t *testing.T) {
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	cases := []struct {
		name string
		want corehttp2.ErrCode
		run  func(*rawH2)
	}{
		{"rapid reset", corehttp2.ErrCodeEnhanceYourCalm, func(c *rawH2) {
			var flood []byte
			//: every pair in one write: past the budget of twice MaxStreams, nothing
			//: finished in between.
			for i := uint32(1); i < 2*600; i += 2 {
				flood = append(flood, frameBytes(corehttp2.FrameHeaders, corehttp2.FlagEndStream|corehttp2.FlagEndHeaders, i, requestBlock("GET", "/x"))...)
				flood = append(flood, frameBytes(corehttp2.FrameRSTStream, 0, i, binary.BigEndian.AppendUint32(nil, uint32(corehttp2.ErrCodeCancel)))...)
			}
			c.write(flood)
		}},
		{"continuation flood", corehttp2.ErrCodeEnhanceYourCalm, func(c *rawH2) {
			flood := frameBytes(corehttp2.FrameHeaders, 0, 1, requestBlock("GET", "/x"))
			//: past the 32-frame bound on one field block, in one write: the
			//: server closes once it refuses, so a frame written after that
			//: would meet a reset, which is not what is tested.
			for range 40 {
				flood = append(flood, frameBytes(corehttp2.FrameContinuation, 0, 1, literalField("x-a", "b"))...)
			}
			c.write(flood)
		}},
		{"hpack bomb", corehttp2.ErrCodeCompression, func(c *rawH2) {
			//: inserts a large value, then references it past the list ceiling.
			block := []byte{0x40, 0x01, 'x'}
			big := strings.Repeat("v", 120)
			block = append(block, byte(len(big)))
			block = append(block, big...)
			//: each index byte expands to the entry, far past the ceiling.
			for range 200 {
				block = append(block, 0x80|62)
			}
			c.send(corehttp2.FrameHeaders, corehttp2.FlagEndStream|corehttp2.FlagEndHeaders, 1, block)
		}},
	}
	for _, b := range loopBackends() {
		for _, tc := range cases {
			t.Run(b.String()+"/"+tc.name, func(t *testing.T) {
				_, addr := startLoopServer(t, b, http2.HandleLoop(hello, http1.Parser{}), Plaintext())
				c := dialRawH2(t, addr)
				tc.run(c)
				if code := c.untilGoAway(); code != tc.want {
					t.Fatalf("GOAWAY code = %v, want %v", code, tc.want)
				}
			})
		}
	}
}

// TestLoopHTTP2_streamsAndWindows is #960's first criterion on real backends:
// a connection holding 100 pending streams answers the 101st HEADERS
// RST_STREAM(REFUSED_STREAM) while the 100 complete; and on another
// connection an unread 240 KiB body on one stream, its bytes buffered, never
// delays a second stream's answer on the same connection.
func TestLoopHTTP2_streamsAndWindows(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			h := newWorkerH()
			_, addr := startLoopServer(t, b, http2.HandleLoop(h, http1.Parser{}), Plaintext())
			c := dialRawH2(t, addr)
			//: 100 streams the worker holds open.
			for i := uint32(1); i <= 199; i += 2 {
				c.get(i, "/slow")
			}
			c.waitJobs(t, h, corehttp2.DefaultMaxStreams)
			c.get(201, "/fast")
			ev, err := c.next()
			if err != nil {
				t.Fatal(err)
			}
			if ev.h.Type != corehttp2.FrameRSTStream || ev.h.StreamID != 201 || corehttp2.ErrCode(binary.BigEndian.Uint32(ev.payload)) != corehttp2.ErrCodeRefusedStream {
				t.Fatalf("101st stream answered %+v", ev.h)
			}
			close(h.release)
			//: the 100 complete, the 101st never reaches the handler.
			done := 0
			for done < 100 {
				ev, err := c.next()
				if err != nil {
					t.Fatalf("after %d answers: %v", done, err)
				}
				if ev.h.Type == corehttp2.FrameData && ev.h.Flags&corehttp2.FlagEndStream != 0 {
					done++
				}
			}
			//: on a fresh connection, an unread body does not delay another answer.
			other := dialRawH2(t, addr)
			other.send(corehttp2.FrameHeaders, corehttp2.FlagEndHeaders, 1, requestBlock("POST", "/upload"))
			chunk := bytes.Repeat([]byte("x"), 16384)
			for range 15 { //: 240 KiB, within the stream window, no END_STREAM.
				other.send(corehttp2.FrameData, 0, 1, chunk)
			}
			other.get(3, "/fast/other")
			if got := other.answerOf(3); !strings.Contains(got, "fast /fast/other") {
				t.Fatalf("stream 3 = %q", got)
			}
			other.send(corehttp2.FrameData, corehttp2.FlagEndStream, 1, chunk[:1])
			if got := other.answerOf(1); !strings.Contains(got, "fast /upload") {
				t.Fatalf("stream 1 = %q", got)
			}
		})
	}
}

// waitJobs waits for the handler to have seen want pending requests.
func (c *rawH2) waitJobs(t *testing.T, h *workerH, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.mu.Lock()
		n := int(h.next)
		h.mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d pending requests arrived, want %d", n, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestLoopHTTP2_bodyBeyondStreamWindow pins a body past the 256 KiB stream
// window, read whole, through a real client: the credit follows the bytes the
// adapter buffered, so the window never stalls a body within its bound.
func TestLoopHTTP2_bodyBeyondStreamWindow(t *testing.T) {
	echo := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		res.Body = append([]byte(nil), req.Body...)
	})
	body := bytes.Repeat([]byte("0123456789"), 40<<10) // 400 KiB
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			//: an output ceiling the 400 KiB echo fits in, as http1's answers do.
			_, addr := startLoopServer(t, b, http2.HandleLoop(echo, http1.Parser{}), Plaintext(), Loop(corenet.LoopValue{Backend: b, OutputCap: 2 << 20}))
			cli := h2cClient(t)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/echo", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := cli.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(resp.Body)
			swallowErr(resp.Body.Close())
			if err != nil {
				t.Fatal(err)
			}
			if resp.ProtoMajor != 2 || !bytes.Equal(got, body) {
				t.Fatalf("proto %d, body %d bytes, want %d", resp.ProtoMajor, len(got), len(body))
			}
		})
	}
}
