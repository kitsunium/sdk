package server

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

const (
	// errorLogCap is how many records of net/http's own log a server writes in
	// one second. The rest are counted, never written: a scanner probing a port
	// must not turn into one line per attempt.
	errorLogCap int = 10
	// errorLogWindow is the span the cap counts in.
	errorLogWindow time.Duration = time.Second
	// errorLogDetailMax clips the lines after the first of one record — a
	// recovered panic's stack — in the record's detail attribute.
	errorLogDetailMax int = 2048
	// errorLogSource is the value of the source attribute every record carries.
	errorLogSource string = "net/http"
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
}

// Write implements io.Writer. It never fails: net/http has nobody to hand a
// failed log to.
func (w *errorLogWriter) Write(p []byte) (int, error) {
	//: no logger: discarded, and counted so State says it happened.
	if w.logger == nil {
		w.budget.suppressed.Add(1)
		//: the line was consumed.
		return len(p), nil
	}
	//: over the cap: counted by the budget.
	if !w.budget.admit(w.clk.Now()) {
		//: the line was consumed.
		return len(p), nil
	}
	first, rest, _ := strings.Cut(strings.TrimRight(string(p), "\r\n"), "\n")
	attrs := []corelogger.AttrValue{
		{Key: "source", Value: corelogger.StringValue(errorLogSource)},
		{Key: "group", Value: corelogger.StringValue(w.group)},
	}
	//: a recovered panic's stack, or any further lines, clipped.
	if rest != "" {
		attrs = append(attrs, corelogger.AttrValue{Key: "detail", Value: corelogger.StringValue(clip(rest, errorLogDetailMax))})
	}
	w.logger.Log(context.Background(), level.Warn, first, attrs...)
	//: the line was consumed.
	return len(p), nil
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
