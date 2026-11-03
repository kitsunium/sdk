package logger_test

import (
	"context"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	svclogger "github.com/kitsunium/sdk/internal/service/observe/logger"
)

// pcRecorder is a Handler that records the PC of every record it handles and,
// when wants is set, asks for it through corelogger.SourceRequester.
type pcRecorder struct {
	mu   sync.Mutex
	pcs  []uintptr
	want *bool
}

func (h *pcRecorder) Enabled(context.Context, corelogger.RecordEvent) bool { return true }
func (h *pcRecorder) Handle(_ context.Context, r corelogger.RecordEvent) error {
	h.mu.Lock()
	h.pcs = append(h.pcs, r.PC)
	h.mu.Unlock()
	return nil
}
func (h *pcRecorder) WithAttrs([]corelogger.AttrValue) corelogger.Handler { return h }
func (h *pcRecorder) WithGroup(string) corelogger.Handler                 { return h }

// askingRecorder is a pcRecorder that implements SourceRequester.
type askingRecorder struct{ pcRecorder }

func (h *askingRecorder) WantsSource() bool                                   { return true }
func (h *askingRecorder) WithAttrs([]corelogger.AttrValue) corelogger.Handler { return h }
func (h *askingRecorder) WithGroup(string) corelogger.Handler                 { return h }

// emitThreeWays emits one record on each emission path.
func emitThreeWays(lg corelogger.Logger) {
	ctx := context.Background()
	lg.Log(ctx, level.Info, "log")
	svclogger.LogAttrs(ctx, lg, level.Info, "attrs", nil)
	svclogger.Build(lg, level.Info).Str("k", "v").Send(ctx, "send")
}

// TestCallerPCOnlyWhenAHandlerAsks (#1014): a handler that does not ask for
// the source receives PC == 0 on Log, LogAttrs and Build.Send — no stack walk —
// and one that asks, directly or under WithCaller, receives the caller's PC.
func TestCallerPCOnlyWhenAHandlerAsks(t *testing.T) {
	t.Parallel()
	plain := &pcRecorder{}
	lg, err := svclogger.New(plain)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	emitThreeWays(lg)
	emitThreeWays(lg.With(corelogger.AttrValue{Key: "a", Value: corelogger.StringValue("b")}))
	for i, pc := range plain.pcs {
		if pc != 0 {
			t.Fatalf("record %d reached a plain handler with PC %#x: the stack was walked for nobody", i, pc)
		}
	}
	asking := &askingRecorder{}
	lg, err = svclogger.New(asking)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	emitThreeWays(lg)
	emitThreeWays(lg.WithGroup("g"))
	if len(asking.pcs) != 6 {
		t.Fatalf("asking handler saw %d records, want 6", len(asking.pcs))
	}
	for i, pc := range asking.pcs {
		if pc == 0 {
			t.Fatalf("record %d reached a handler that asked for the source with PC 0", i)
		}
	}
}

// TestWithCallerStillCapturesThePC (#1014): the PC reaches the handler under
// WithCaller on each path, and the plain handler beneath the decorator is what
// the decorator forwards to.
func TestWithCallerStillCapturesThePC(t *testing.T) {
	t.Parallel()
	inner := &pcRecorder{}
	base, err := svclogger.New(inner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	emitThreeWays(svclogger.WithCaller(base, 0))
	if len(inner.pcs) != 3 {
		t.Fatalf("saw %d records, want 3", len(inner.pcs))
	}
	for i, pc := range inner.pcs {
		if pc == 0 {
			t.Fatalf("record %d under WithCaller carried PC 0", i)
		}
	}
}
