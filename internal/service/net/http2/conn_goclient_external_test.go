package http2_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// laneServer serves the real client through Lane: a stand-in for the loop
// adapter (#961), reading from a socket what the adapter will read from the
// loop, and answering what the handler returns.
type laneServer struct {
	// ln listens on the loopback.
	ln net.Listener
	// handler answers each complete request.
	handler func(path string, body []byte) (int, string, []byte)
	// done closes when the accept loop ends.
	done chan struct{}
	// wg counts the connection goroutines.
	wg sync.WaitGroup
	// mu guards conns.
	mu sync.Mutex
	// conns holds the live connections.
	conns []net.Conn
}

// newLaneServer starts a server on an ephemeral loopback port.
func newLaneServer(t *testing.T, handler func(string, []byte) (int, string, []byte)) *laneServer {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &laneServer{ln: ln, handler: handler, done: make(chan struct{})}
	go s.accept()
	return s
}

func (s *laneServer) accept() {
	defer close(s.done)
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serve(c)
	}
}

// close stops the server and every connection.
func (s *laneServer) close() {
	_ = s.ln.Close()
	<-s.done
	s.mu.Lock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// laneRequest gathers a request across HEADERS and DATA events.
type laneRequest struct {
	path string
	body []byte
}

// serve drives one connection: read into a spill, Feed, serve the events,
// write out, credit, end the batch.
func (s *laneServer) serve(c net.Conn) {
	defer s.wg.Done()
	defer func() { _ = c.Close() }()
	lane, err := http2.NewLane(corehttp2.LimitsValue{}, 1<<30, 1<<24)
	if err != nil {
		return
	}
	var conn http2.Conn
	lane.Open(&conn)
	defer lane.Close(&conn)
	var req corenet.HTTP1RequestValue
	scratch := make([]byte, 0, 4096)
	reqs := map[uint32]*laneRequest{}
	spill := make([]byte, 0, 64*1024)
	buf := make([]byte, 64*1024)
	out := make([]byte, 0, 256*1024)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			spill = append(spill, buf[:n]...)
			used, grown := lane.Feed(&conn, spill, out[:0])
			out = grown
			var failed bool
			out, scratch, failed = s.handle(lane, &conn, reqs, &req, scratch, out)
			out = lane.AppendCredits(&conn, out)
			if len(out) > 0 {
				if _, werr := c.Write(out); werr != nil {
					return
				}
			}
			lane.EndBatch()
			scratch = scratch[:0]
			spill = append(spill[:0], spill[used:]...)
			if failed || lane.Closing(&conn) {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// handle serves one Feed's events, in order: decode, accumulate, answer.
func (s *laneServer) handle(lane *http2.Lane, conn *http2.Conn, reqs map[uint32]*laneRequest, req *corenet.HTTP1RequestValue, scratch, out []byte) ([]byte, []byte, bool) {
	for _, ev := range lane.Events() {
		var err error
		switch ev.Kind {
		case http2.EventHeaders:
			scratch, err = lane.Decode(conn, ev.Data, scratch, req)
			if err != nil {
				out = lane.Abort(conn, corehttp2.ErrCodeCompression, out)
				return out, scratch, true
			}
			if ev.Flags&http2.EventDiscard != 0 {
				continue
			}
			path := fieldOf(req, ":path")
			reqs[ev.Stream] = &laneRequest{path: path}
			if ev.Flags&http2.EventEndStream != 0 {
				out = s.answer(lane, conn, ev.Stream, reqs, out)
			}
		case http2.EventData:
			r := reqs[ev.Stream]
			if r != nil {
				r.body = append(r.body, ev.Data...)
				lane.Consume(conn, ev.Stream, len(ev.Data))
			}
			if ev.Flags&http2.EventEndStream != 0 && r != nil {
				out = s.answer(lane, conn, ev.Stream, reqs, out)
			}
		case http2.EventReset:
			delete(reqs, ev.Stream)
		}
	}
	return out, scratch, false
}

// fieldOf returns a decoded field's value.
func fieldOf(req *corenet.HTTP1RequestValue, name string) string {
	for i := range req.NFields {
		if string(req.Fields[i].Name) == name {
			return string(req.Fields[i].Value)
		}
	}
	return ""
}

// answer runs the handler and writes the response for stream.
func (s *laneServer) answer(lane *http2.Lane, conn *http2.Conn, stream uint32, reqs map[uint32]*laneRequest, out []byte) []byte {
	r := reqs[stream]
	delete(reqs, stream)
	status, hdr, body := s.handler(r.path, r.body)
	res := &corenet.HTTP1ResponseValue{Status: status, Header: []byte(hdr), Body: body}
	out, err := lane.Headers(conn, stream, res, len(body) == 0, out)
	if err != nil {
		return out
	}
	if len(body) > 0 {
		out = lane.Send(conn, stream, body, out, true)
	}
	return out
}

// TestLaneWithGoClient runs the legal paths through a Go http.Transport (h2c,
// prior knowledge): GET, a POST with a body read whole, a large response past
// the 65 535-byte initial window that the hold resumes on WINDOW_UPDATE,
// and concurrent streams.
func TestLaneWithGoClient(t *testing.T) {
	big := bytes.Repeat([]byte("abcdefghij"), 30000) // 300 KB, past the initial window
	s := newLaneServer(t, func(path string, body []byte) (int, string, []byte) {
		switch {
		case strings.HasPrefix(path, "/echo"):
			return 200, "Content-Type: text/plain\r\n", body
		case path == "/big":
			return 200, "", big
		default:
			return 200, "X-Path: " + path + "\r\n", []byte("hello " + path)
		}
	})
	defer s.close()
	client, stop := h2cClient(t)
	defer stop()
	addr := s.ln.Addr().String()

	t.Run("GET", func(t *testing.T) {
		resp, err := client.Get("http://" + addr + "/hello")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.ProtoMajor != 2 || string(body) != "hello /hello" || resp.Header.Get("X-Path") != "/hello" {
			t.Fatalf("proto %d body %q header %v", resp.ProtoMajor, body, resp.Header)
		}
	})
	t.Run("POST body", func(t *testing.T) {
		payload := bytes.Repeat([]byte("p"), 128*1024)
		resp, err := client.Post("http://"+addr+"/echo", "text/plain", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || !bytes.Equal(body, payload) {
			t.Fatalf("echo: err %v, %d bytes", err, len(body))
		}
	})
	t.Run("large response", func(t *testing.T) {
		resp, err := client.Get("http://" + addr + "/big")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || !bytes.Equal(body, big) {
			t.Fatalf("big: err %v, %d bytes", err, len(body))
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for i := range 16 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				path := fmt.Sprintf("/c/%d", i)
				resp, err := client.Get("http://" + addr + path)
				if err != nil {
					errs <- err
					return
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				if resp.ProtoMajor != 2 || string(body) != "hello "+path {
					errs <- fmt.Errorf("%s: %d %q", path, resp.ProtoMajor, body)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
	})
}
