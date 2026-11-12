// Package server_test — net/http's own log, routed through the server's
// logger instead of stderr (#846).
package server_test

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// recorded is one record a recording logger received.
type recorded struct {
	// level and message are the record's.
	level   level.Level
	message string
	// attrs are its attributes, by key, as strings.
	attrs map[string]string
}

// recordingLogger is a corelogger.Logger that keeps what it is given.
type recordingLogger struct {
	mu      sync.Mutex
	records []recorded
}

// Log implements corelogger.Logger.
func (r *recordingLogger) Log(_ context.Context, lv level.Level, msg string, attrs ...corelogger.AttrValue) {
	rec := recorded{level: lv, message: msg, attrs: map[string]string{}}
	for _, a := range attrs {
		rec.attrs[a.Key] = a.Value.String()
	}
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
}

// With implements corelogger.Logger.
func (r *recordingLogger) With(...corelogger.AttrValue) corelogger.Logger { return r }

// WithGroup implements corelogger.Logger.
func (r *recordingLogger) WithGroup(string) corelogger.Logger { return r }

// Enabled implements corelogger.Logger.
func (r *recordingLogger) Enabled(context.Context, level.Level) bool { return true }

// got returns a copy of the records so far.
func (r *recordingLogger) got() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.records...)
}

// guardedBuffer is a bytes.Buffer safe to write from net/http's goroutines.
type guardedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer.
func (b *guardedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String is what was written.
func (b *guardedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureStandardLog points the process's standard logger at a buffer for the
// test, which is where net/http writes when its server has no ErrorLog: the
// stand-in for stderr, which a test cannot rebind. It restores the output
// after, so a test using it runs alone.
func captureStandardLog(t *testing.T) *guardedBuffer {
	t.Helper()
	buf := &guardedBuffer{}
	previous := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return buf
}

// panicOnce serves a group on loopback whose handler panics, and makes one
// request to it, answered by a closed connection.
func panicOnce(t *testing.T, opts ...server.Option) *server.Server {
	t.Helper()
	srv := server.New(opts...)
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.Plaintext()).
		HandleHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("canary") }))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	client := &http.Client{Timeout: boundsDeadline}
	response, err := client.Get("http://" + srv.State().Listeners[0].Address + "/")
	if err == nil {
		closeOrFail(t, response.Body)
	}
	return srv
}

// TestWithLogger_netHTTPReachesTheLoggerNotStderr pins the audit's case with
// the line net/http logs from its own goroutine in this engine: a handler
// panic net/http recovers. The configured logger receives one warn record with
// source=net/http, the group's name and the stack in detail, and the standard
// logger — what net/http falls back to, and stderr's stand-in — stays empty.
func TestWithLogger_netHTTPReachesTheLoggerNotStderr(t *testing.T) {
	std := captureStandardLog(t)
	rec := &recordingLogger{}
	panicOnce(t, server.WithLogger(rec))
	if !waitFor(t, func() bool { return len(rec.got()) > 0 }) {
		t.Fatal("the logger received no record")
	}
	records := rec.got()
	if len(records) != 1 {
		t.Fatalf("the logger received %d records, want 1: %+v", len(records), records)
	}
	r := records[0]
	if r.level != level.Warn || !strings.HasPrefix(r.message, "http: panic serving") || !strings.Contains(r.message, "canary") {
		t.Errorf("record = %q at %v, want a warn naming the panic", r.message, r.level)
	}
	if r.attrs["source"] != "net/http" || r.attrs["group"] != "api" {
		t.Errorf("attrs = %v, want source=net/http and group=api", r.attrs)
	}
	if !strings.Contains(r.attrs["detail"], "goroutine") {
		t.Errorf("detail = %q, want the panic's stack", r.attrs["detail"])
	}
	if got := std.String(); got != "" {
		t.Errorf("net/http wrote to the standard logger: %q", got)
	}
}

// TestWithLogger_noLoggerDiscardsAndCounts pins the default: no logger, no
// stderr — the line is discarded and State counts it.
func TestWithLogger_noLoggerDiscardsAndCounts(t *testing.T) {
	std := captureStandardLog(t)
	srv := panicOnce(t)
	if !waitFor(t, func() bool { return srv.State().SuppressedLogs == 1 }) {
		t.Fatalf("SuppressedLogs = %d, want 1", srv.State().SuppressedLogs)
	}
	if got := std.String(); got != "" {
		t.Errorf("net/http wrote to the standard logger with no logger given: %q", got)
	}
}

// TestWithLogger_nilLoggerIsTheDefault pins that a nil logger is no logger.
func TestWithLogger_nilLoggerIsTheDefault(t *testing.T) {
	std := captureStandardLog(t)
	srv := panicOnce(t, server.WithLogger(nil))
	if !waitFor(t, func() bool { return srv.State().SuppressedLogs == 1 }) {
		t.Fatalf("SuppressedLogs = %d, want 1", srv.State().SuppressedLogs)
	}
	if got := std.String(); got != "" {
		t.Errorf("net/http wrote to the standard logger: %q", got)
	}
}

var _ io.Writer = (*guardedBuffer)(nil)
