package kit

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/security/redact"
)

// Logs: what product code writes through kit.Log is, in dev, also kept by
// the hub — the last maxLogs records — and streamed to the Studio, so a
// request's logs show beside its spans.

const (
	// maxLogs is how many records the hub keeps.
	maxLogs int = 2000
	// defaultLogs is how many records the log list gives when the Studio
	// does not say.
	defaultLogs int = 200
	// maxLogText bounds a message and each attribute's text.
	maxLogText int = 2048
	// maxLogAttrs bounds the attributes kept per record.
	maxLogAttrs int = 32
)

// terminalText bounds a message and each attribute's text on the terminal:
// generously, so that a long message or a stack stays whole.
const terminalText int = 64 << 10

// logLevels ranks the model's levels, for the minimum-level filter.
var logLevels = rankOf([]string{"debug", "info", "warn", "error"})

// redactingEncoder is the product logger's encoder. Before the SDK's encoder
// writes a record, it hides what the Studio's ring hides: an attribute named
// like a secret, the credentials of a URL, and the personal, special and
// secret members of a value logged whole — so a struct logged whole writes
// no address into a log aggregator. The SDK's encoder receives the whole
// record (Append), so no SDK change is needed; the record the other sinks
// receive is left as it was.
type redactingEncoder struct{ logger.Encoder }

// logRing is the sink that hands records to the hub.
type logRing struct {
	hub   *hub
	clock clock.Clock
}

// logFilter selects records; a zero field selects everything.
type logFilter struct {
	trace, node string
	// level is the minimum level's rank.
	level int
}

// productLog is the logger kit.Log hands out.
func (a *App) productLog() logger.Logger {
	if a.rt.plog != nil {
		return a.rt.plog
	}
	return a.log
}

// productLogger builds the logger product code writes through: the app's
// sink, at the app's level, and in dev the hub's ring too — at every level,
// so the Studio can show the debug records the terminal does not. What it
// writes to the terminal is redacted as the ring is (ADR 0006): its encoder
// is the app's, wrapped.
func (a *App) productLogger(sink logger.Sink, enc logger.Encoder, level logger.Level) logger.Logger {
	enc = redactingEncoder{enc}
	var cfg logger.SinkConfig
	if !a.hub.enabled {
		cfg = logger.SinkConfig{Sink: sink, Encoder: enc, MinLevel: level}
	} else {
		tee := logger.Multi(logger.LevelGate(sink, level), &logRing{hub: a.hub, clock: a.clock})
		cfg = logger.SinkConfig{Sink: tee, Encoder: enc, MinLevel: logger.LevelDebug}
	}
	lg, err := logger.NewWithSink(cfg)
	if err != nil {
		return a.log
	}
	return lg.With(logger.String("app", a.name))
}

// Append writes r, redacted, through the wrapped encoder.
func (e redactingEncoder) Append(dst []byte, groups []string, r logger.Record) []byte {
	r.Message = redactor.Text(r.Message, terminalText)
	if len(r.Attrs) > 0 {
		prefix := strings.Join(groups, ".")
		attrs := make([]logger.Attr, len(r.Attrs))
		for i, at := range r.Attrs {
			attrs[i] = redactAttr(prefix, at)
		}
		r.Attrs = attrs
	}
	return e.Encoder.Append(dst, groups, r)
}

// redactAttr is one attribute as the terminal may write it. A name is judged
// under its groups, "db.password", as the ring judges it; a string loses the
// credentials of its URLs; a value logged whole becomes its JSON with its
// classified members redacted, an error what describe says of it. Other
// scalars, and groups — which the SDK's encoders do not open — pass as they
// are.
func redactAttr(prefix string, at logger.Attr) logger.Attr {
	key := at.Key
	if prefix != "" && key != "" {
		key = prefix + "." + key
	}
	if key != "" && redactor.Name(key) {
		return logger.String(at.Key, redact.Placeholder)
	}
	switch at.Value.Kind() {
	case logger.KindString:
		return logger.String(at.Key, redactor.Text(at.Value.String(), terminalText))
	case logger.KindAny:
		for _, text := range redactor.Attrs([]logger.Attr{at}, terminalText) {
			return logger.String(at.Key, text)
		}
	default:
		// Every other kind of value is kept as it is.
	}
	return at
}

// Write keeps the record: its level, message, node and trace, and its
// attributes as text, secrets redacted. The time is the app's — the dev
// clock the Studio may have moved — so a record lines up with its spans.
func (r *logRing) Write(ctx context.Context, rec logger.Record, p []byte) (int, error) {
	lr := model.LogRecord{
		Time:    r.clock.Now().UTC(),
		Level:   levelName(rec.Level),
		Message: redactor.Text(rec.Message, maxLogText),
	}
	if tc := rec.TraceContext; tc.IsValid() {
		lr.TraceID, lr.SpanID = hex.EncodeToString(tc.TraceID[:]), hex.EncodeToString(tc.SpanID[:])
	}
	var shown []logger.Attr
	lr.Node, shown = nodeAttr(rec.Attrs)
	lr.Attrs = shownAttrs(shown)
	if lr.Node == "" {
		lr.Node = currentNode(ctx)
	}
	r.hub.log(lr)
	return len(p), nil
}

// nodeAttr takes the node a record names out of its attributes, and drops
// the ones every record of the app carries.
func nodeAttr(attrs []logger.Attr) (node string, rest []logger.Attr) {
	for _, at := range attrs {
		switch at.Key {
		case "node":
			if node == "" && at.Value.Kind() == logger.KindString {
				node = at.Value.String()
				continue
			}
		case "app", "framework_version":
			continue
		}
		rest = append(rest, at)
	}
	return node, rest
}

// shownAttrs are the attributes the Studio shows, at most maxLogAttrs: a
// group flattens into dotted keys, a structured value becomes JSON, and a
// secret — by its key, or by a field tagged kit:"secret" — is redacted.
func shownAttrs(attrs []logger.Attr) map[string]string {
	var out map[string]string
	for key, text := range redactor.Attrs(attrs, maxLogText) {
		if len(out) >= maxLogAttrs {
			break
		}
		if out == nil {
			out = map[string]string{}
		}
		out[key] = text
	}
	return out
}

// Flush does nothing: the ring holds every record it was given.
func (r *logRing) Flush(_ context.Context) error { return nil }

// Close does nothing: the ring holds no resource.
func (r *logRing) Close() error { return nil }

// levelName is the record's level as the model spells it.
func levelName(l logger.Level) string {
	switch {
	case l < logger.LevelInfo:
		return "debug"
	case l < logger.LevelWarn:
		return "info"
	case l < logger.LevelError:
		return "warn"
	}
	return "error"
}

// log keeps a record and streams it.
func (h *hub) log(rec model.LogRecord) {
	if !h.enabled {
		return
	}
	h.mu.Lock()
	h.logSeq++
	rec.Seq = h.logSeq
	if len(h.logs) < maxLogs {
		h.logs = append(h.logs, rec)
	} else {
		h.logs[h.logNext] = rec
		h.logNext = (h.logNext + 1) % maxLogs
	}
	h.mu.Unlock()
	h.publish(model.Event{Type: model.EventLog, Time: rec.Time, Log: &rec})
}

// match reports whether the record r passes the filter.
func (f logFilter) match(r *model.LogRecord) bool {
	return (f.trace == "" || r.TraceID == f.trace) &&
		(f.node == "" || r.Node == f.node) &&
		logLevels[r.Level] >= f.level
}

// recentLogs returns the last limit records that match, oldest first.
func (h *hub) recentLogs(f logFilter, limit int) []model.LogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(h.logs)
	out := make([]model.LogRecord, 0, min(limit, n))
	for j := n - 1; j >= 0 && len(out) < limit; j-- {
		r := &h.logs[(h.logNext+j)%n]
		if f.match(r) {
			out = append(out, *r)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// serveLogs answers GET /_kit/api/logs?trace=&node=&level=&limit=.
func (a *App) serveLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := logFilter{trace: q.Get("trace"), node: q.Get("node")}
	if lv := strings.ToLower(q.Get("level")); lv != "" {
		rank, ok := logLevels[lv]
		if !ok {
			a.replyError(r.Context(), w, Invalid("level must be debug, info, warn or error"))
			return
		}
		f.level = rank
	}
	limit := defaultLogs
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLogs {
			a.replyError(r.Context(), w, Invalid("limit must be a number from 1 to "+strconv.Itoa(maxLogs)))
			return
		}
		limit = n
	}
	writeJSON(w, http.StatusOK, a.hub.recentLogs(f, limit))
}

// rankOf ranks words by their place in order, from 0.
func rankOf(order []string) map[string]int {
	ranks := make(map[string]int, len(order))
	for i, w := range order {
		ranks[w] = i
	}
	return ranks
}
