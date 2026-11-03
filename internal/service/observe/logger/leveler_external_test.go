package logger_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/memory"
)

// TestALiveFloorMovesWithoutARebuild (#1015): a handler gated on a level.Var
// drops Debug, then — after Set(Debug), nothing rebuilt — emits it, on the
// logger and on a child derived with With and WithGroup.
func TestALiveFloorMovesWithoutARebuild(t *testing.T) {
	t.Parallel()
	floor := level.NewVar(level.Info)
	sink := memory.NewMemory()
	h, err := svclogger.NewHandlerLeveled(encoder.NewText(clock.System), sink, floor)
	if err != nil {
		t.Fatalf("NewHandlerLeveled: %v", err)
	}
	lg, err := svclogger.New(h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	child := lg.With(corelogger.AttrValue{Key: "c", Value: corelogger.StringValue("1")}).WithGroup("g")
	ctx := context.Background()
	lg.Log(ctx, level.Debug, "dropped")
	child.Log(ctx, level.Debug, "dropped too")
	if n := len(sink.Records()); n != 0 {
		t.Fatalf("%d Debug records passed an Info floor", n)
	}
	floor.Set(level.Debug)
	lg.Log(ctx, level.Debug, "kept")
	child.Log(ctx, level.Debug, "kept too")
	if n := len(sink.Records()); n != 2 {
		t.Fatalf("%d records after Set(Debug), want 2", n)
	}
	floor.Set(level.Error)
	if lg.Enabled(ctx, level.Warn) || child.Enabled(ctx, level.Warn) {
		t.Fatal("Warn enabled after Set(Error)")
	}
}

// TestALiveFloorOnTheTextHandler (#1015): the legacy text handler reads its
// floor live too.
func TestALiveFloorOnTheTextHandler(t *testing.T) {
	t.Parallel()
	floor := level.NewVar(level.Warn)
	var buf bytes.Buffer
	h, err := svclogger.NewTextHandlerLeveled(&buf, floor)
	if err != nil {
		t.Fatalf("NewTextHandlerLeveled: %v", err)
	}
	lg, err := svclogger.New(h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Log(context.Background(), level.Info, "first")
	floor.Set(level.Info)
	lg.Log(context.Background(), level.Info, "second")
	if out := buf.String(); strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("output %q", out)
	}
}

// TestANilFloorIsInfo: a nil Leveler gates at Info, the zero MinLevel.
func TestANilFloorIsInfo(t *testing.T) {
	t.Parallel()
	h, err := svclogger.NewHandlerLeveled(encoder.NewText(clock.System), memory.NewMemory(), nil)
	if err != nil {
		t.Fatalf("NewHandlerLeveled: %v", err)
	}
	ctx := context.Background()
	if h.Enabled(ctx, corelogger.RecordEvent{Level: level.Debug}) || !h.Enabled(ctx, corelogger.RecordEvent{Level: level.Info}) {
		t.Fatal("a nil floor is not Info")
	}
}
