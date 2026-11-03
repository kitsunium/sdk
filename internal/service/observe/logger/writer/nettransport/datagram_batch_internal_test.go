package nettransport

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
)

// TestUDPBatchSendsOneDatagramPerRecord (#807): the udp seam has a batch
// call — sendmmsg(2) on Linux — and each record of a backlog still leaves as
// its own datagram, in order.
func TestUDPBatchSendsOneDatagramPerRecord(t *testing.T) {
	t.Parallel()
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer server.Close()
	send, sendBatch, closer, err := newConnSeam(protoUDP, server.LocalAddr().String(), nil)
	if err != nil {
		t.Fatalf("newConnSeam: %v", err)
	}
	if sendBatch == nil {
		t.Fatal("the udp seam has no batch call")
	}
	s := newNetSink(protoUDP, send, closer)
	s.sendBatch = sendBatch
	defer s.Close()
	payloads := [][]byte{[]byte("one\n"), []byte("two\n"), []byte("three\n")}
	if written, werr := s.WriteBatch(t.Context(), make([]corelogger.RecordEvent, 3), payloads); werr != nil || written != 3 {
		t.Fatalf("WriteBatch = %d, %v; want 3, nil", written, werr)
	}
	if err := server.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 1024)
	for _, want := range payloads {
		n, _, rerr := server.ReadFrom(buf)
		if rerr != nil || string(buf[:n]) != string(want) {
			t.Fatalf("datagram = %q, %v; want %q", buf[:n], rerr, want)
		}
	}
}

// ndjsonCollector records every POST's content type and body.
type ndjsonCollector struct {
	mu    sync.Mutex
	posts [][2]string
}

// ServeHTTP records the request.
func (c *ndjsonCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.posts = append(c.posts, [2]string{r.Header.Get("Content-Type"), string(body)})
	c.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// TestHTTPBatchNDJSONPostsOneBodyPerBatch (#807): with HTTPBatchNDJSON the
// http seam posts a backlog as ONE application/x-ndjson body, a record per
// line — a record without its newline gains one; without it the seam has no
// batch call and posts record by record, as before.
func TestHTTPBatchNDJSONPostsOneBodyPerBatch(t *testing.T) {
	t.Parallel()
	collector := &ndjsonCollector{}
	server := httptest.NewServer(collector)
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	if _, sendBatch, _ := newHTTPSeams(server.URL, client, false); sendBatch != nil {
		t.Fatal("the http seam batches without HTTPBatchNDJSON: the wire would change unasked")
	}
	send, sendBatch, closer := newHTTPSeams(server.URL, client, true)
	if sendBatch == nil {
		t.Fatal("HTTPBatchNDJSON gave the http seam no batch call")
	}
	s := newNetSink(protoHTTP, send, closer)
	s.sendBatch = sendBatch
	payloads := [][]byte{[]byte(`{"msg":"one"}` + "\n"), []byte(`{"msg":"two"}`)}
	if written, werr := s.WriteBatch(t.Context(), make([]corelogger.RecordEvent, 2), payloads); werr != nil || written != 2 {
		t.Fatalf("WriteBatch = %d, %v; want 2, nil", written, werr)
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.posts) != 1 {
		t.Fatalf("%d POSTs, want one for the batch", len(collector.posts))
	}
	if got := collector.posts[0]; got[0] != contentTypeNDJSON || got[1] != `{"msg":"one"}`+"\n"+`{"msg":"two"}`+"\n" {
		t.Fatalf("POST = %q %q", got[0], got[1])
	}
	if !strings.HasPrefix(contentTypeNDJSON, "application/x-ndjson") {
		t.Fatalf("content type %q", contentTypeNDJSON)
	}
}
