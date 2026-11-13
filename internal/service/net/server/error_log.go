package server

import (
	"cmp"
	"context"
	"log"
	stdnet "net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

const (
	// errorLogCap is how many records of net/http's own log, and of the TLS
	// handshakes the engine saw fail, a server writes in one second. The rest are counted, never written: a scanner probing a port
	// must not turn into one line per attempt.
	errorLogCap int = 10
	// errorLogWindow is the span the cap counts in.
	errorLogWindow time.Duration = time.Second
	// errorLogDetailMax clips the lines after the first of one record — a
	// recovered panic's stack — in the record's detail attribute.
	errorLogDetailMax int = 2048
	// errorLogSource is the value of the source attribute every record of an
	// HTTP group carries: net/http's own lines, and the TLS handshake failures
	// the engine negotiates in net/http's place.
	errorLogSource string = "net/http"
	// handshakeLogSource is the source of a TLS handshake failure on a group
	// net/http does not serve: a connection handler's, or a loop codec's.
	handshakeLogSource string = "net/server"
)

// errorLogBudget is the server's one budget of net/http log records, shared by
// every HTTP group: the cap is a property of the process's log, not of a port.
type errorLogBudget struct {
	// mu guards the window.
	mu sync.Mutex
	// start is when the current window opened.
	start time.Time
	// used counts the records the window has written.
	used int
	// suppressed counts every record not written — over the cap, or with no
	// logger to write it to — for State.
	suppressed atomic.Uint64
}

// admit reports whether one more record may be written at now, opening a new
// window once the last has elapsed. A record not admitted is counted.
func (b *errorLogBudget) admit(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: the window ran out, or none was open yet: a new one starts here.
	if b.start.IsZero() || now.Sub(b.start) >= errorLogWindow {
		b.start, b.used = now, 0
	}
	//: the window is full.
	if b.used >= errorLogCap {
		b.suppressed.Add(1)
		//: counted, not written.
		return false
	}
	b.used++
	//: written.
	return true
}

// errorLogWriter is the io.Writer behind http.Server.ErrorLog: what net/http
// reports from its own goroutines — a recovered handler panic, an HTTP/2
// stream error, a superfluous WriteHeader, a failed accept — as a warn record
// of the SDK's logger with source=net/http, under the server's budget. With no
// logger it discards, counting: net/http's default is the process's standard
// logger, plain text on stderr outside the structured stream.
type errorLogWriter struct {
	// logger receives the records; nil discards and counts.
	logger corelogger.Logger
	// group names the group whose http.Server wrote the line.
	group string
	// budget is the server's, shared by its groups.
	budget *errorLogBudget
	// clk is the server's clock, the budget's window.
	clk clock.Clock
	// source is the source attribute of the records: empty for net/http, on
	// an HTTP group, net/server for the handshake failures of any other.
	source string
}

// Write implements io.Writer. It never fails: net/http has nobody to hand a
// failed log to.
func (w *errorLogWriter) Write(p []byte) (int, error) {
	w.record(string(p))
	//: the line was consumed.
	return len(p), nil
}

// record writes line as one warn record under the budget, or counts it: with
// no logger, or over the cap.
func (w *errorLogWriter) record(line string) {
	//: no logger: discarded, and counted so State says it happened.
	if w.logger == nil {
		w.budget.suppressed.Add(1)
		//: counted.
		return
	}
	//: over the cap: counted by the budget.
	if !w.budget.admit(w.clk.Now()) {
		//: counted.
		return
	}
	first, rest, _ := strings.Cut(strings.TrimRight(line, "\r\n"), "\n")
	attrs := []corelogger.AttrValue{
		{Key: "source", Value: corelogger.StringValue(cmp.Or(w.source, errorLogSource))},
		{Key: "group", Value: corelogger.StringValue(w.group)},
	}
	//: a recovered panic's stack, or any further lines, clipped.
	if rest != "" {
		attrs = append(attrs, corelogger.AttrValue{Key: "detail", Value: corelogger.StringValue(clip(rest, errorLogDetailMax))})
	}
	w.logger.Log(context.Background(), level.Warn, first, attrs...)
}

// clip bounds s to n bytes.
func clip(s string, n int) string {
	//: within the bound.
	if len(s) <= n {
		//: as it is.
		return s
	}
	//: cut at the bound.
	return s[:n]
}

// errorLog is the *log.Logger a group's http.Server writes to: the server's
// logger and budget behind an io.Writer, with no prefix and no flags — the
// record carries its own time.
func (s *Server) errorLog(group string) *log.Logger {
	return log.New(&errorLogWriter{logger: s.logger, group: group, budget: &s.errBudget, clk: s.clk}, "", 0)
}

// logWriter is the server's logger and budget for group's records, with
// source as their source attribute.
func (s *Server) logWriter(group, source string) *errorLogWriter {
	//: the server's logger, budget and clock.
	return &errorLogWriter{logger: s.logger, group: group, budget: &s.errBudget, clk: s.clk, source: source}
}

// handshakeLog is the writer a TLS group's handshake failures go to (#846):
// the engine negotiates TLS itself, before net/http or a codec sees the
// connection, so net/http's own "TLS handshake error" line is never written
// and the engine writes it in its place — under the same budget, with the
// same fields. An HTTP group's records say source=net/http, any other
// group's source=net/server.
func (s *Server) handshakeLog(group *StreamGroup) *errorLogWriter {
	//: a group with no identity never negotiates.
	if group.identity.IsZero() {
		//: none.
		return nil
	}
	source := handshakeLogSource
	//: net/http serves the group: the line it would have written.
	if group.httpAdapter != nil {
		source = ""
	}
	//: the server's writer.
	return s.logWriter(group.name, source)
}

// handshakeFailed records one TLS negotiation that failed with err from
// remote, as net/http words it on an HTTP group — "http: TLS handshake error
// from <addr>: <reason>" — and without its "http: " on any other, the reason crypto/tls's error, which names no key, certificate
// content or byte the peer sent. A handshake the engine itself gave up — its
// context cancelled by the drain or the stop — is not a failure of the peer,
// and is not recorded. A nil writer records nothing.
func (w *errorLogWriter) handshakeFailed(ctx context.Context, remote stdnet.Addr, err error) {
	//: no writer, or the engine's own cancel.
	if w == nil || ctx.Err() != nil {
		//: nothing.
		return
	}
	addr := "unknown"
	//: the peer's address, when the socket has one.
	if remote != nil {
		addr = remote.String()
	}
	prefix := "TLS handshake error from "
	//: net/http's own wording on its own groups.
	if w.source == "" {
		prefix = "http: " + prefix
	}
	//: through the budget, as net/http's own lines are.
	w.record(prefix + addr + ": " + err.Error())
}
