package logger_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	svclogger "github.com/kitsunium/sdk/internal/service/observe/logger"
)

// tracedKey marks the context a test emits under as carrying a span.
type tracedKey struct{}

// fixedSpan is the span every traced emission is under.
var fixedSpan = corelogger.TraceContextValue{
	TraceID: [corelogger.TraceIDLen]byte{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
	SpanID:  [corelogger.SpanIDLen]byte{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
}

// spanSource reads fixedSpan off a context marked tracedKey, the zero pair
// off any other.
func spanSource(ctx context.Context) corelogger.TraceContextValue {
	if ctx.Value(tracedKey{}) != nil {
		return fixedSpan
	}
	return corelogger.TraceContextValue{}
}

// TestEveryDerivedLoggerKeepsItsTraceSource: a Logger built with a trace
// source and then rebuilt — WithCaller, With, WithGroup (named or empty, on
// an owner or not), each composed with the others — still stamps the span of
// the context a record is emitted under. WithCaller used to rebuild the
// Logger with no source, so every record after it lost trace_id and span_id.
func TestEveryDerivedLoggerKeepsItsTraceSource(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), tracedKey{}, true)
	derivations := map[string]func(corelogger.Logger) corelogger.Logger{
		"WithCaller": func(lg corelogger.Logger) corelogger.Logger { return svclogger.WithCaller(lg, 0) },
		"With": func(lg corelogger.Logger) corelogger.Logger {
			return lg.With(corelogger.AttrValue{Key: "k", Value: corelogger.StringValue("v")})
		},
		"WithGroup":       func(lg corelogger.Logger) corelogger.Logger { return lg.WithGroup("g") },
		"WithGroup empty": func(lg corelogger.Logger) corelogger.Logger { return lg.WithGroup("") },
		"WithCaller then With": func(lg corelogger.Logger) corelogger.Logger {
			return svclogger.WithCaller(lg, 0).With(corelogger.AttrValue{Key: "k", Value: corelogger.StringValue("v")})
		},
		"With then WithCaller": func(lg corelogger.Logger) corelogger.Logger {
			return svclogger.WithCaller(lg.With(corelogger.AttrValue{Key: "k", Value: corelogger.StringValue("v")}), 0)
		},
		"WithGroup then WithCaller": func(lg corelogger.Logger) corelogger.Logger {
			return svclogger.WithCaller(lg.WithGroup("g"), 0).WithGroup("h")
		},
		"owned, WithGroup empty": func(lg corelogger.Logger) corelogger.Logger {
			return svclogger.Owning(lg, nopCloser{}).WithGroup("")
		},
	}
	for name, derive := range derivations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			h, err := svclogger.NewTextHandler(&buf, level.Debug)
			if err != nil {
				t.Fatalf("NewTextHandler: %v", err)
			}
			lg, err := svclogger.NewWithTraceContext(h, spanSource)
			if err != nil {
				t.Fatalf("NewWithTraceContext: %v", err)
			}
			derive(lg).Log(ctx, level.Info, "traced")
			line := buf.String()
			for _, want := range []string{"trace_id=4bf92f3577b34da6a3ce929d0e0e4736", "span_id=00f067aa0ba902b7"} {
				if !strings.Contains(line, want) {
					t.Fatalf("%s lost the trace source: %q lacks %s", name, line, want)
				}
			}
		})
	}
}

// nopCloser is an io.Closer with nothing to release.
type nopCloser struct{}

// Close releases nothing.
func (nopCloser) Close() error { return nil }
