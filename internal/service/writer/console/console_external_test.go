package console_test

import (
	"io"
	"os"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/internal/service/writer/console"
)

// captureStream swaps *target (os.Stdout or os.Stderr) for a pipe, runs fn,
// restores the original FD, and returns everything fn wrote to the stream. It
// gives the black-box tests a real os-level I/O assertion without mocking the
// sink — NewStdout/NewStderr bind to the process FDs directly, so capture has
// to happen at the FD, not behind an injected writer.
func captureStream(t *testing.T, target **os.File, fn func()) string {
	t.Helper()
	//: a real pipe stands in for the terminal FD for the duration of fn.
	rd, wr, err := os.Pipe()
	//: a failed pipe is an environment fault, not a logic path under test.
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	//: redirect the process stream to the pipe's write end, remembering the
	//: original so it can be restored even if fn writes nothing.
	orig := *target
	*target = wr
	//: always restore the real FD so a failure here cannot leak into siblings.
	defer func() { *target = orig }()
	//: drive the production write while the stream is redirected.
	fn()
	//: closing the write end signals EOF so the drain below terminates.
	if cerr := wr.Close(); cerr != nil {
		t.Fatalf("close pipe writer: %v", cerr)
	}
	//: drain everything fn emitted onto the stream.
	out, rerr := io.ReadAll(rd)
	//: a drain failure means the assertion target is unreadable — fail loud.
	if rerr != nil {
		t.Fatalf("drain pipe: %v", rerr)
	}
	//: hand back the captured bytes for the caller's content assertion.
	return string(out)
}

func TestConsoleRegistered(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"importing the package registers the console writer"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: the blank import must have self-registered the factory.
		if !writer.Name("console").Known() {
			t.Errorf("console writer not registered after import")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestConsoleOpen(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		cfg     writer.Config
		wantErr bool
	}
	tests := []tc{
		{"stdout default", writer.ConsoleConfig{}, false},
		{"stderr stream", writer.ConsoleConfig{Stream: writer.ConsoleStderr}, false},
		{"per-writer error floor", writer.ConsoleConfig{MinLevel: level.Error}, false},
		{"wrong config type rejected", writer.FileConfig{Path: "/x"}, true},
		//: a stream selector outside {stdout, stderr} must fail fast, not
		//: silently default to stdout.
		{"out-of-range stream rejected", writer.ConsoleConfig{Stream: writer.ConsoleStream(99)}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := writer.Open("console", c.cfg)
		//: the failure arm must surface an error and a nil sink.
		if c.wantErr {
			if err == nil || sink != nil {
				t.Errorf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
			}
			return
		}
		//: the happy arm must build a usable sink.
		if err != nil || sink == nil {
			t.Errorf("%s: err=%v sink=%v want nil+sink", c.name, err, sink)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestConsoleOpen_Write(t *testing.T) {
	//: this test swaps the process-global os.Stdout FD, so it must not run in
	//: parallel with any other stdout-capturing test — concurrent FD swaps race.
	type tc struct {
		name      string
		lvl       level.Level
		wantBytes bool
	}
	tests := []tc{
		//: a Debug record sits below the Warn floor — the gate drops it as a
		//: successful no-op, so nothing reaches stdout.
		{"below floor is dropped", level.Debug, false},
		//: a Warn record is at the floor — it passes the gate and hits stdout.
		{"at floor passes through", level.Warn, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var (
			n     int
			werr  error
			oerr  error
			sink  corelogger.Sink
			payld = []byte("console-write-payload\n")
		)
		//: Open AND Write both run with stdout redirected: NewStdout binds the
		//: process FD at construction time, so the sink must be built after the
		//: swap or it would capture the real terminal FD.
		out := captureStream(t, &os.Stdout, func() {
			//: build the gated stdout sink via the public registry entry point.
			sink, oerr = writer.Open("console", writer.ConsoleConfig{Stream: writer.ConsoleStdout, MinLevel: level.Warn})
			//: a nil sink here would panic the Write below — guard before use.
			if oerr != nil || sink == nil {
				return
			}
			n, werr = sink.Write(t.Context(), corelogger.RecordEvent{Level: c.lvl}, payld)
		})
		//: a registry/open failure means there is no sink to exercise.
		if oerr != nil || sink == nil {
			t.Fatalf("%s: Open err=%v sink=%v want nil+sink", c.name, oerr, sink)
		}
		//: Write must never surface an error on either the drop or pass arm.
		if werr != nil {
			t.Fatalf("%s: Write err=%v want nil", c.name, werr)
		}
		//: a below-floor record is dropped: the gate reports the payload as
		//: accepted (n==len(p), the multi-fanout no-fail contract) but nothing
		//: actually reaches stdout.
		if !c.wantBytes {
			if n != len(payld) || out != "" {
				t.Errorf("%s: n=%d out=%q want %d and empty", c.name, n, out, len(payld))
			}
			return
		}
		//: a passed record reports a positive count and the payload reaches stdout.
		if n <= 0 || out != string(payld) {
			t.Errorf("%s: n=%d out=%q want >0 and %q", c.name, n, out, payld)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

func TestConsoleOpen_WriteStderr(t *testing.T) {
	//: swaps the process-global os.Stderr FD — kept non-parallel for the same
	//: reason as TestConsoleOpen_Write.
	type tc struct {
		name string
	}
	tests := []tc{{"a passing record routes to stderr end-to-end"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var (
			n     int
			werr  error
			oerr  error
			sink  corelogger.Sink
			payld = []byte("stderr-routed-payload\n")
		)
		//: Open after the stderr swap so NewStderr binds the redirected FD; a
		//: Debug floor lets every record through so the route, not the gate, is
		//: what this case exercises.
		out := captureStream(t, &os.Stderr, func() {
			sink, oerr = writer.Open("console", writer.ConsoleConfig{Stream: writer.ConsoleStderr, MinLevel: level.Debug})
			//: a nil sink would panic the Write below — guard before use.
			if oerr != nil || sink == nil {
				return
			}
			n, werr = sink.Write(t.Context(), corelogger.RecordEvent{Level: level.Warn}, payld)
		})
		//: no sink means nothing to assert the stderr route against.
		if oerr != nil || sink == nil {
			t.Fatalf("%s: Open err=%v sink=%v want nil+sink", c.name, oerr, sink)
		}
		//: the stderr write must succeed and land the payload on the stderr pipe.
		if werr != nil || n <= 0 || out != string(payld) {
			t.Errorf("%s: n=%d err=%v out=%q want >0,nil,%q", c.name, n, werr, out, payld)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

func TestConsoleOpen_Flush(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"flush is a no-op through the delegation chain"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a default config exercises the levelgate inherit path (no wrapper).
		sink, err := writer.Open("console", writer.ConsoleConfig{})
		//: no sink means the delegation chain cannot be exercised.
		if err != nil || sink == nil {
			t.Fatalf("%s: Open err=%v sink=%v want nil+sink", c.name, err, sink)
		}
		//: Flush must walk levelgate → consolesink and report a clean no-op.
		if ferr := sink.Flush(t.Context()); ferr != nil {
			t.Errorf("%s: Flush err=%v want nil", c.name, ferr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestConsoleOpen_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"close is a no-op through the delegation chain"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a default config exercises the levelgate inherit path (no wrapper).
		sink, err := writer.Open("console", writer.ConsoleConfig{})
		//: no sink means the delegation chain cannot be exercised.
		if err != nil || sink == nil {
			t.Fatalf("%s: Open err=%v sink=%v want nil+sink", c.name, err, sink)
		}
		//: Close must delegate to consolesink, which never closes the owned FD.
		if cerr := sink.Close(); cerr != nil {
			t.Errorf("%s: Close err=%v want nil", c.name, cerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
