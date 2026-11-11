package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

const (
	// varAccessLog is <PRODUCT>_ACCESS_LOG's stem: which requests the access
	// record is written for.
	varAccessLog string = "ACCESS_LOG"
	// slowPrefix opens the mode that logs the slow requests: slow:100ms.
	slowPrefix string = "slow:"
)

// accessMode is which requests an access record is written for.
type accessMode uint8

const (
	// accessAll writes the record of every request. It is the zero value.
	accessAll accessMode = iota
	// accessErrors writes it for a 4xx or a 5xx answer.
	accessErrors
	// accessSlow writes it for a request that took longer than slow.
	accessSlow
	// accessOff writes none.
	accessOff
)

// accessPolicy is <PRODUCT>_ACCESS_LOG as resolved: the mode, and for
// accessSlow the duration a request must exceed.
type accessPolicy struct {
	mode accessMode
	slow time.Duration
}

// String is the policy as <PRODUCT>_ACCESS_LOG spells it.
func (p accessPolicy) String() string {
	switch p.mode {
	case accessErrors:
		return "errors"
	case accessSlow:
		return slowPrefix + p.slow.String()
	case accessOff:
		return "off"
	default:
		return "all"
	}
}

// logs reports whether a request that was answered with status after took is
// written down.
func (p accessPolicy) logs(status int, took time.Duration) bool {
	switch p.mode {
	case accessErrors:
		return status >= http.StatusBadRequest
	case accessSlow:
		return took > p.slow
	case accessOff:
		return false
	default:
		return true
	}
}

// parseAccessLog reads what an operator wrote: all, errors, off, or
// slow:<duration> with a positive duration. ok is false for anything else.
func parseAccessLog(raw string) (p accessPolicy, ok bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "all":
		return accessPolicy{mode: accessAll}, true
	case "errors":
		return accessPolicy{mode: accessErrors}, true
	case "off":
		return accessPolicy{mode: accessOff}, true
	}
	text, found := strings.CutPrefix(raw, slowPrefix)
	if !found {
		return accessPolicy{}, false
	}
	d, err := time.ParseDuration(text)
	if err != nil || d <= 0 {
		return accessPolicy{}, false
	}
	return accessPolicy{mode: accessSlow, slow: d}, true
}

// resolveAccessLog is the access policy <PRODUCT>_ACCESS_LOG names: every
// request outside dev, where a log line is read by an aggregator, and the
// errors in dev, where the Studio shows every request. A value that is none
// of the modes refuses the start, naming the variable: a typo must not
// silently turn the record off.
func resolveAccessLog(name string, own func(stem string) string, dev bool) (accessPolicy, error) {
	raw := own(varAccessLog)
	if strings.TrimSpace(raw) == "" {
		if dev {
			return accessPolicy{mode: accessErrors}, nil
		}
		return accessPolicy{mode: accessAll}, nil
	}
	p, ok := parseAccessLog(raw)
	if !ok {
		variable := productVariable(name, varAccessLog)
		return accessPolicy{}, failure(CodeAppConfig, "ACCESS_LOG_INVALID",
			variable+" is all, errors, off or slow:<duration>, such as slow:100ms", nil,
			errs.String("variable", variable), errs.String("value", clip(raw)))
	}
	return p, nil
}

// countingBody counts the bytes the handler reads from a request's body.
type countingBody struct {
	io.ReadCloser
	n int64
}

// Read reads from the body and counts what it gave.
func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	return n, err
}

// accessWriter remembers the status a handler sent and the bytes of its body.
// It is the response writer of the middleware that writes the access record,
// so it keeps what net/http gives a handler's writer: Flush, ReadFrom — the
// sendfile path of a static file — and, through Unwrap, the controller's
// deadlines and hijacking.
type accessWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	// flushErr is the last refusal of a Flush.
	flushErr error
}

// WriteHeader records the status and writes it.
func (w *accessWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write counts the body; a write with no status before it is a 200.
func (w *accessWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// ReadFrom copies r to the response, through the wrapped writer's own
// ReadFrom when it has one.
func (w *accessWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	var n int64
	var err error
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(r)
	} else {
		n, err = io.Copy(struct{ io.Writer }{w.ResponseWriter}, r)
	}
	w.bytes += n
	return n, err
}

// Flush sends what is buffered. The status line it may send is a 200 when
// the handler said nothing, which writeAccess reads from a status never
// recorded. A writer that cannot flush sends the response when the handler
// ends: nothing is lost but the early send, which flushErr remembers.
func (w *accessWriter) Flush() {
	w.flushErr = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap is the writer the accessWriter wraps, for http.ResponseController.
func (w *accessWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// account serves r under an identifier — the caller's X-Request-Id when it is
// usable, a UUIDv7 otherwise —, echoes it on the response, and writes the
// request's access record when it ends, whatever the handler did, a panic
// that aborts the response included.
func (a *App) account(w http.ResponseWriter, r *http.Request, h http.Handler) {
	begun := a.clock.Now()
	info := &requestInfo{}
	if rid, ok := requestIDOf(r.Header); ok {
		info.id = rid
	} else if rid, ok := mintRequestID(); ok {
		info.id = rid
	}
	if info.id != "" {
		w.Header().Set(requestIDHeader, info.id)
	}
	r = r.WithContext(context.WithValue(r.Context(), requestKey{}, info))
	var in *countingBody
	if r.Body != nil && r.Body != http.NoBody {
		in = &countingBody{ReadCloser: r.Body}
		r.Body = in
	}
	out := &accessWriter{ResponseWriter: w}
	defer func() {
		var read int64
		if in != nil {
			read = in.n
		}
		a.writeAccess(r, info, out, read, a.clock.Now().Sub(begun))
	}()
	h.ServeHTTP(out, r)
}

// writeAccess writes the request's access record when the policy asks for
// it. The route is the pattern the router matched — never the raw path, whose
// ids would make every line its own series — and r.Pattern is set by the
// router on the request it was given, which is r. A request no route
// matched has none.
func (a *App) writeAccess(r *http.Request, info *requestInfo, out *accessWriter, bytesIn int64, took time.Duration) {
	status := out.status
	if status == 0 {
		status = http.StatusOK
	}
	if !a.cfg.accessLog.logs(status, took) {
		return
	}
	attrs := make([]logger.Attr, 0, 10)
	attrs = append(attrs,
		logger.String("method", r.Method),
		logger.String("route", routeOf(r.Pattern)),
		logger.Int("status", status),
		logger.Float64("duration_ms", round2(float64(took)/float64(time.Millisecond))),
		logger.Int64("bytes_in", bytesIn),
		logger.Int64("bytes_out", out.bytes),
		logger.String("request_id", info.id),
		logger.String("client", clientAddr(r, a.cfg.trustedProxies)),
		logger.String("user_agent", clipRunes(r.UserAgent(), maxUserAgent)),
	)
	// The logger writes trace_id and span_id itself — the keys it reserves
	// (ADR 0070) — from the span context of its context: the request's span.
	// The caller may have left: it drops a record whose context is done.
	ctx := context.WithoutCancel(r.Context())
	if info.span.IsValid() {
		ctx = trace.ContextWithSpanContext(ctx, info.span)
	}
	logger.Info(ctx, a.productLog(), "request", attrs...)
}

// routeOf is the path of a ServeMux pattern: "GET /items/{id}" is
// "/items/{id}". The method is a field of its own.
func routeOf(pattern string) string {
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}
