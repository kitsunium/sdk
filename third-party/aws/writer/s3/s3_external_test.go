package s3_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/third-party/aws/writer/s3"
)

// extProvider is a public-side CredentialProvider for the black-box test.
type extProvider struct{}

// : compile-time proof the provider satisfies the credential port.
var _ writer.CredentialProvider = (*extProvider)(nil)

func (extProvider) Credentials(_ context.Context) (writer.CredentialValue, error) {
	//: fixed material; the external test never performs a real upload.
	return writer.NewCredentialValue("AKIAEXT", "secret", ""), nil
}

func TestOpenViaRegistry(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		cfg      writer.Config
		wantErr  bool
		wantSink bool
	}
	tests := []tc{
		{"valid config resolves and builds a sink", writer.S3Config{Bucket: "b", Region: "eu-west-3", Credentials: extProvider{}}, false, true},
		{"wrong config type is rejected", writer.FileConfig{Path: "/x"}, true, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := writer.Open("s3", c.cfg)
		//: failure arm — error + nil sink.
		if c.wantErr {
			if err == nil || sink != nil {
				t.Errorf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
			}
			return
		}
		//: happy arm — a usable sink built offline; release it afterward.
		if err != nil || sink == nil {
			t.Fatalf("%s: err=%v sink=%v want nil+sink", c.name, err, sink)
		}
		//: surface a close failure rather than discarding it.
		if cerr := sink.Close(); cerr != nil {
			t.Errorf("%s: close: %v", c.name, cerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// recordingS3 is a real in-process HTTP server standing in for S3. It captures
// the raw PutObject request bodies so the test can assert the exact bytes the
// production chain put on the wire.
type recordingS3 struct {
	mu     sync.Mutex
	bodies []string
}

// handler answers every PutObject with an empty 200 (what the SDK needs to
// treat the upload as a success) and records the request body verbatim.
func (r *recordingS3) handler(w http.ResponseWriter, req *http.Request) {
	//: read the object bytes the SDK streamed over the socket.
	body, err := io.ReadAll(req.Body)
	//: a socket read failure fails the upload so the test surfaces it.
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	r.mu.Lock()
	r.bodies = append(r.bodies, string(body))
	r.mu.Unlock()
	//: an ETag header + empty 200 is the minimal success PutObject accepts.
	w.Header().Set("ETag", `"e2e"`)
	w.WriteHeader(http.StatusOK)
}

// joined returns every captured body concatenated; the fake-S3 chunk framing
// (used by some SigV4 payload modes) wraps the object bytes, so the test
// asserts containment rather than byte equality.
func (r *recordingS3) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.bodies, "")
}

func (r *recordingS3) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func TestS3Sink_EndToEndOverRealSocket(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		lines []string
	}
	tests := []tc{
		//: two records coalesce into one PutObject flushed to the live server.
		{"two records reach the socket as one object", []string{"alpha\n", "beta\n"}},
		//: a single record still travels the full Write→Flush→PutObject path.
		{"single record travels end-to-end", []string{"solo\n"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rec := &recordingS3{}
		//: a real stdlib HTTP server is the S3 endpoint — genuine socket I/O.
		srv := httptest.NewServer(http.HandlerFunc(rec.handler))
		t.Cleanup(srv.Close)
		//: build the production chain (async+levelgate+s3Sink) via the registry,
		//: pointed at the live server through the path-style Endpoint override.
		sink, err := writer.Open("s3", writer.S3Config{
			Bucket:      "e2e-bucket",
			Region:      "eu-west-3",
			Endpoint:    srv.URL,
			Credentials: extProvider{},
		})
		if err != nil {
			t.Fatalf("%s: Open: %v", c.name, err)
		}
		//: drive the real Sink.Write path for every record.
		for _, l := range c.lines {
			if n, werr := sink.Write(t.Context(), corelogger.RecordEvent{}, []byte(l)); werr != nil || n != len(l) {
				t.Fatalf("%s: Write(%q)=(%d,%v)", c.name, l, n, werr)
			}
		}
		//: Flush forces the batch through async to a real PutObject over the wire.
		if ferr := sink.Flush(t.Context()); ferr != nil {
			t.Fatalf("%s: Flush: %v", c.name, ferr)
		}
		//: Close joins the async drainer and uploads any straggler batch.
		if cerr := sink.Close(); cerr != nil {
			t.Fatalf("%s: Close: %v", c.name, cerr)
		}
		//: at least one real PutObject must have hit the server.
		if rec.count() == 0 {
			t.Fatalf("%s: no PutObject reached the server", c.name)
		}
		//: every record's bytes must be present in what the socket received.
		got := rec.joined()
		for _, l := range c.lines {
			if !strings.Contains(got, strings.TrimSuffix(l, "\n")) {
				t.Errorf("%s: server body %q missing record %q", c.name, got, l)
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
