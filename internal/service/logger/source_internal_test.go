// Package logger — white-box tests for the source-location decorator.
package logger

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// captureHere returns the program counter and line number of its own call site,
// mirroring how the front-end captures a single frame so resolution can be
// exercised against a deterministic, in-test location.
func captureHere() (pc uintptr, line int) {
	//: capture the immediate caller frame the way the front-end does.
	var pcs [1]uintptr
	n := runtime.Callers(1, pcs[:])
	//: guard the slice access on the callers count.
	if n == 0 {
		//: report the miss so callers can skip the assertion.
		return 0, 0
	}
	//: resolve the frame to read its line for the expectation.
	frame, _ := runtime.CallersFrames([]uintptr{pcs[0]}).Next()
	return pcs[0], frame.Line
}

// recordingHandler is a stub corelogger.Handler that records the last record it
// handled so decorator tests can assert the attrs threaded through it.
type recordingHandler struct {
	// last is the most recent RecordEvent passed to Handle.
	last corelogger.RecordEvent
	// attrs is the attrs slice captured by the most recent WithAttrs call.
	attrs []corelogger.AttrValue
	// group is the name captured by the most recent WithGroup call.
	group string
}

// Enabled always reports true so Handle is always exercised.
func (h *recordingHandler) Enabled(_ context.Context, _ corelogger.RecordEvent) bool {
	//: the stub never gates — every record reaches Handle.
	return true
}

// Handle records the supplied RecordEvent for later assertion.
func (h *recordingHandler) Handle(_ context.Context, r corelogger.RecordEvent) error {
	//: stash the record so the test can inspect the threaded attrs.
	h.last = r
	//: the stub never fails.
	return nil
}

// WithAttrs records attrs and returns the receiver for chaining.
func (h *recordingHandler) WithAttrs(attrs []corelogger.AttrValue) corelogger.Handler {
	//: record the bound attrs so the decorator's threading can be asserted.
	h.attrs = attrs
	//: return the receiver so the rewrap can be observed.
	return h
}

// WithGroup records name and returns the receiver for chaining.
func (h *recordingHandler) WithGroup(name string) corelogger.Handler {
	//: record the group name so the decorator's threading can be asserted.
	h.group = name
	//: return the receiver so the rewrap can be observed.
	return h
}

// Test_newSourceHandler verifies nil rejection, skip clamping, and success.
func Test_newSourceHandler(t *testing.T) {
	t.Parallel()

	//: table of inputs and the expected construction outcome.
	tests := []struct {
		name     string
		next     corelogger.Handler
		skip     int
		wantErr  bool
		wantSkip int
	}{
		{name: "nil next rejected", next: nil, skip: 0, wantErr: true, wantSkip: 0},
		{name: "zero skip accepted", next: &recordingHandler{}, skip: 0, wantErr: false, wantSkip: 0},
		{name: "positive skip retained", next: &recordingHandler{}, skip: 3, wantErr: false, wantSkip: 3},
		{name: "negative skip clamped", next: &recordingHandler{}, skip: -5, wantErr: false, wantSkip: 0},
	}

	//: construct each case and assert the outcome.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: build the decorator under test.
			got, err := newSourceHandler(tc.next, tc.skip)

			//: an error expectation short-circuits the success assertions.
			if tc.wantErr {
				//: the nil-next path must return the documented sentinel.
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			//: the success path must not error.
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			//: the concrete decorator must carry the clamped skip.
			sh, ok := got.(*sourceHandler)
			//: guard the type assertion before reading the skip field.
			if !ok {
				t.Fatalf("expected *sourceHandler, got %T", got)
			}

			//: the retained skip must match the clamped expectation.
			if sh.skip != tc.wantSkip {
				t.Fatalf("got skip %d want %d", sh.skip, tc.wantSkip)
			}
		})
	}
}

// Test_sourceHandler_Enabled verifies the decorator defers to the inner handler.
func Test_sourceHandler_Enabled(t *testing.T) {
	t.Parallel()

	//: table describing the single delegation invariant.
	tests := []struct {
		name string
		want bool
	}{
		{name: "delegates to inner handler", want: true},
	}

	//: exercise each case in its own subtest.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: wrap a recording handler that always reports enabled.
			h, err := newSourceHandler(&recordingHandler{}, 0)
			//: guard construction before exercising Enabled.
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			//: the decorator must mirror the inner handler's decision.
			if got := h.Enabled(t.Context(), corelogger.RecordEvent{}); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// Test_sourceHandler_Handle verifies the source attr is appended when the PC
// resolves and omitted when it is zero.
func Test_sourceHandler_Handle(t *testing.T) {
	t.Parallel()

	//: capture a real call site to feed a resolvable program counter.
	pc, _ := captureHere()

	//: table of records and the expected presence of the source attr.
	tests := []struct {
		name       string
		pc         uintptr
		wantSource bool
	}{
		{name: "resolvable pc appends source", pc: pc, wantSource: true},
		{name: "zero pc omits source", pc: 0, wantSource: false},
	}

	//: handle each record and inspect the threaded attrs.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: wrap a recording handler so the threaded record is observable.
			rec := &recordingHandler{}
			h, err := newSourceHandler(rec, 0)
			//: guard construction before exercising Handle.
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			//: handle a record carrying the program counter under test.
			hErr := h.Handle(t.Context(), corelogger.RecordEvent{
				Level: level.Info,
				PC:    tc.pc,
			})
			//: the stub handler never fails.
			if hErr != nil {
				t.Fatalf("unexpected handle error: %v", hErr)
			}

			//: scan the threaded attrs for the source key.
			found := false
			//: walk every attr to detect the source annotation.
			for _, a := range rec.last.Attrs {
				//: match against the well-known source key.
				if a.Key == SourceFieldKey {
					found = true
				}
			}

			//: the presence of the source attr must match the expectation.
			if found != tc.wantSource {
				t.Fatalf("source present=%v want %v", found, tc.wantSource)
			}
		})
	}
}

// Test_sourceHandler_WithAttrs verifies the derived handler stays decorated.
func Test_sourceHandler_WithAttrs(t *testing.T) {
	t.Parallel()

	//: table describing the rewrap invariant.
	tests := []struct {
		name string
	}{
		{name: "derived handler stays a sourceHandler"},
	}

	//: exercise each case in its own subtest.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: wrap a recording handler and derive an attrs child.
			h, err := newSourceHandler(&recordingHandler{}, 2)
			//: guard construction before deriving.
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			//: derive a child carrying a bound attr.
			child := h.WithAttrs([]corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}})

			//: the derived handler must remain a source decorator.
			if _, ok := child.(*sourceHandler); !ok {
				t.Fatalf("expected *sourceHandler child, got %T", child)
			}
		})
	}
}

// Test_sourceHandler_WithGroup verifies the derived handler stays decorated.
func Test_sourceHandler_WithGroup(t *testing.T) {
	t.Parallel()

	//: table describing the rewrap invariant.
	tests := []struct {
		name string
	}{
		{name: "derived handler stays a sourceHandler"},
	}

	//: exercise each case in its own subtest.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: wrap a recording handler and derive a group child.
			h, err := newSourceHandler(&recordingHandler{}, 1)
			//: guard construction before deriving.
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			//: derive a child namespaced under a group.
			child := h.WithGroup("svc")

			//: the derived handler must remain a source decorator.
			if _, ok := child.(*sourceHandler); !ok {
				t.Fatalf("expected *sourceHandler child, got %T", child)
			}
		})
	}
}

// Test_withSource verifies appending honours resolvability.
func Test_withSource(t *testing.T) {
	t.Parallel()

	//: capture a real call site to feed a resolvable program counter.
	pc, _ := captureHere()

	//: table of append inputs and the expected resulting field count.
	tests := []struct {
		name      string
		pc        uintptr
		base      []corelogger.AttrValue
		wantCount int
		wantLast  bool
	}{
		{name: "zero pc leaves attrs untouched", pc: 0, base: nil, wantCount: 0, wantLast: false},
		{name: "resolvable pc appends one attr", pc: pc, base: nil, wantCount: 1, wantLast: true},
		{
			name:      "resolvable pc appends after existing",
			pc:        pc,
			base:      []corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}},
			wantCount: 2,
			wantLast:  true,
		},
	}

	//: exercise each case in its own subtest.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: append a resolved source to the base attrs.
			got := withSource(tc.base, tc.pc)

			//: the resulting attr count must match the expectation.
			if len(got) != tc.wantCount {
				t.Fatalf("got %d attrs want %d", len(got), tc.wantCount)
			}

			//: when an attr was appended it must be the trailing source key.
			if tc.wantLast {
				//: the last attr carries the source annotation.
				if got[len(got)-1].Key != SourceFieldKey {
					t.Fatalf("last attr key %q want %q", got[len(got)-1].Key, SourceFieldKey)
				}
			}
		})
	}
}

// Test_resolveFrame verifies a captured PC resolves and a zero PC does not.
func Test_resolveFrame(t *testing.T) {
	t.Parallel()

	//: capture a real call site within this test file.
	pc, line := captureHere()

	//: table of resolver inputs and expected resolution outcomes.
	tests := []struct {
		name     string
		pc       uintptr
		wantOK   bool
		wantLine int
	}{
		{name: "zero pc does not resolve", pc: 0, wantOK: false, wantLine: 0},
		{name: "captured pc resolves to a frame", pc: pc, wantOK: true, wantLine: line},
	}

	//: resolve each program counter and assert the outcome.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: resolve the program counter under test.
			frame, ok := resolveFrame(tc.pc)

			//: the resolvability flag must match the expectation.
			if ok != tc.wantOK {
				t.Fatalf("got ok=%v want %v", ok, tc.wantOK)
			}

			//: a miss carries no further assertions.
			if !tc.wantOK {
				return
			}

			//: the resolved file must point at this test file.
			if !strings.HasSuffix(frame.File, "source_internal_test.go") {
				t.Fatalf("file %q does not end in source_internal_test.go", frame.File)
			}

			//: the resolved line must match the captured call site.
			if frame.Line != tc.wantLine {
				t.Fatalf("got line %d want %d", frame.Line, tc.wantLine)
			}

			//: the resolved function must name the capture helper.
			if !strings.HasSuffix(frame.Function, "captureHere") {
				t.Fatalf("function %q does not end in captureHere", frame.Function)
			}
		})
	}
}

// Test_renderFrame verifies the canonical file:line:function rendering.
func Test_renderFrame(t *testing.T) {
	t.Parallel()

	//: table of frames and their expected rendering.
	tests := []struct {
		name  string
		frame runtime.Frame
		want  string
	}{
		{
			name:  "fully populated frame",
			frame: runtime.Frame{File: "/a/b.go", Line: 42, Function: "pkg.Fn"},
			want:  "/a/b.go:42:pkg.Fn",
		},
		{
			name:  "zero frame",
			frame: runtime.Frame{},
			want:  ":0:",
		},
	}

	//: render each frame and compare to the expectation.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: the rendered string must match the canonical form.
			if got := renderFrame(tc.frame); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// Test_captureHere verifies the helper returns a usable PC and line.
func Test_captureHere(t *testing.T) {
	t.Parallel()

	//: table describing the single capture invariant.
	tests := []struct {
		name string
	}{
		{name: "capture yields a non-zero pc"},
	}

	//: exercise the capture helper in each case.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			//: capture a program counter and its line.
			pc, line := captureHere()

			//: the captured program counter must be non-zero.
			if pc == 0 {
				t.Fatal("expected non-zero pc")
			}

			//: the captured line must be a positive number.
			if line <= 0 {
				t.Fatalf("expected positive line, got %s", strconv.Itoa(line))
			}
		})
	}
}
