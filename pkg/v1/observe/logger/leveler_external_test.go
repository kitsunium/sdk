package logger_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	corewriter "github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger/slogbridge"
)

// levelAmbiguousCode is 0.3.1.5, LevelAmbiguous.
const levelAmbiguousCode errs.Code = 0x00_03_01_05

// liveFloorSink records into its own buffer, for FromConfigWithLevel.
var liveFloorSink = &bufferSink{buf: &bytes.Buffer{}}

var _ = corewriter.Register(&plainTestFactory{sink: liveFloorSink, registeredAs: "live-floor"})

// TestSinkConfigLevelMovesTheFloorLive (#1015): a Logger built over a LevelVar
// drops Debug, then emits it after Set(Debug) — nothing rebuilt — through the
// Logger, a With-derived child, and a slog.Logger over the bridge.
func TestSinkConfigLevelMovesTheFloorLive(t *testing.T) {
	t.Parallel()
	floor := logger.NewLevelVar(logger.LevelInfo)
	sink := logger.NewMemorySink()
	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, Level: floor})
	if err != nil {
		t.Fatalf("NewWithSink: %v", err)
	}
	child := lg.With(logger.String("child", "1"))
	sl, err := slogbridge.New(lg)
	if err != nil {
		t.Fatalf("slogbridge.New: %v", err)
	}
	ctx := context.Background()
	lg.Log(ctx, logger.LevelDebug, "a")
	child.Log(ctx, logger.LevelDebug, "b")
	sl.DebugContext(ctx, "c")
	if n := len(sink.Records()); n != 0 {
		t.Fatalf("%d Debug records passed an Info floor", n)
	}
	floor.Set(logger.LevelDebug)
	lg.Log(ctx, logger.LevelDebug, "a")
	child.Log(ctx, logger.LevelDebug, "b")
	sl.DebugContext(ctx, "c")
	if n := len(sink.Records()); n != 3 {
		t.Fatalf("%d records after Set(Debug), want 3", n)
	}
	if !sl.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("the bridge's Enabled does not follow the live floor")
	}
}

// TestSinkConfigRefusesTwoFloors (#1015): Level and a non-zero MinLevel
// together are LevelAmbiguous.
func TestSinkConfigRefusesTwoFloors(t *testing.T) {
	t.Parallel()
	_, err := logger.NewWithSink(logger.SinkConfig{Sink: logger.NewMemorySink(), Level: logger.NewLevelVar(logger.LevelInfo), MinLevel: logger.LevelWarn})
	if !errs.HasCode(err, levelAmbiguousCode) {
		t.Fatalf("NewWithSink with two floors = %v, want LevelAmbiguous (0.3.1.5)", err)
	}
}

// TestFromConfigWithLevelSeedsAndFollowsTheVar (#1015): the topology's level
// seeds the caller's LevelVar, and a Set retunes the built Logger.
func TestFromConfigWithLevelSeedsAndFollowsTheVar(t *testing.T) {
	t.Parallel()
	floor := logger.NewLevelVar(logger.LevelDebug)
	lg, err := logger.FromConfigWithLevel(logger.Format("json"), []byte(`{"level":"warn","writers":[{"name":"live-floor","config":{}}]}`), floor)
	if err != nil {
		t.Fatalf("FromConfigWithLevel: %v", err)
	}
	if floor.Level() != logger.LevelWarn {
		t.Fatalf("the topology's level did not seed the var: %v", floor.Level())
	}
	ctx := context.Background()
	lg.Log(ctx, logger.LevelInfo, "below-warn")
	floor.Set(logger.LevelInfo)
	lg.Log(ctx, logger.LevelInfo, "after-set")
	liveFloorSink.mu.Lock()
	out := liveFloorSink.buf.String()
	liveFloorSink.mu.Unlock()
	if strings.Contains(out, "below-warn") || !strings.Contains(out, "after-set") {
		t.Fatalf("output %q", out)
	}
}
