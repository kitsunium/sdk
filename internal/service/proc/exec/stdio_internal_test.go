// Package exec — the stdio wiring. Every function here manages a descriptor or a
// goroutine, and the failure modes are the two that are hardest to see: output
// that is silently truncated, and a copier that outlives the process it was
// draining.
package exec

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// failingWriter reports a fixed error after accepting n bytes, standing in for a
// caller sink that fills up mid-capture.
type failingWriter struct {
	accept int
	err    error
	got    bytes.Buffer
}

// Write accepts up to accept bytes in total, then reports the configured
// failure — the shape a sink that fills up mid-copy actually has.
func (w *failingWriter) Write(p []byte) (int, error) {
	room := w.accept - w.got.Len()
	//: no room left: every further write fails.
	if room <= 0 {
		return 0, w.err
	}
	//: a partial write is what a filling sink does, and io.Copy treats the
	//: accompanying error as fatal to the copy.
	if len(p) > room {
		//: bytes.Buffer.Write never fails, so the count is exact.
		n, werr := w.got.Write(p[:room])
		swallowErr(werr)
		return n, w.err
	}
	return w.got.Write(p)
}

// Test_stdioField pins the annotation that names WHICH stream failed to wire.
// Three pipes are created per capture spawn, and without the field a failure
// reads "could not start the process" with nothing to act on.
func Test_stdioField(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		stream string
	}
	tests := []tc{
		{"the input stream", "stdin"},
		{"the primary output", "stdout"},
		{"the diagnostic output", "stderr"},
		{"the null device", "null"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		f := stdioField(c.stream)
		if f.Key() != "stdio" {
			t.Errorf("the field key is %q, want stdio", f.Key())
		}
		if f.StringValue() != c.stream {
			t.Errorf("the field value is %q, want %q", f.StringValue(), c.stream)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_swallowErr pins the deliberate discard. It exists so the error audit can
// tell a considered drop from a forgotten one, and it must never panic on any
// input — it runs on cleanup paths where a second failure has nowhere to go.
func Test_swallowErr(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		err  error
	}
	tests := []tc{
		{"no error", nil},
		{"a plain error", errors.New("already closed")},
		{"a wrapped error", wrapSpawn(os.ErrClosed)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: reaching the next statement is the assertion.
		swallowErr(c.err)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_recordCopyErr pins the latch. Several copiers can fail at once
// — a full disk breaks stdout and stderr together — and reporting the LAST one
// would make the error depend on goroutine scheduling.
func Test_stdioState_recordCopyErr(t *testing.T) {
	t.Parallel()
	first := errors.New("the first failure")
	second := errors.New("the second failure")

	type tc struct {
		name   string
		errors []error
		want   error
	}
	tests := []tc{
		{name: "nothing recorded"},
		{name: "a single failure", errors: []error{first}, want: first},
		{name: "nil then a failure", errors: []error{nil, first}, want: first},
		{name: "the first of two wins", errors: []error{first, second}, want: first},
		{name: "only successes", errors: []error{nil, nil}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := &stdioState{}
		for _, err := range c.errors {
			s.recordCopyErr(err)
		}
		if got := s.copyError(); !errors.Is(got, c.want) {
			t.Errorf("copyError() = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_copyError pins the read side of the latch under concurrency,
// which is the shape it actually sees: several copier goroutines writing while
// Wait reads.
func Test_stdioState_copyError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		writers int
	}
	tests := []tc{
		{"a single writer", 1},
		{"several concurrent writers", 16},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := &stdioState{}
		//: Goroutine lifecycle: one goroutine per writer, each recording once
		//: and returning; wg.Go joins them all before the assertion below, so
		//: none can outlive this case.
		var wg sync.WaitGroup
		for i := range c.writers {
			wg.Go(func() {
				//: half report a failure, half a success.
				if i%2 == 0 {
					s.recordCopyErr(errors.New("a copier failed"))
					return
				}
				s.recordCopyErr(nil)
			})
		}
		//: read concurrently with the writes; under -race this is where an
		//: unguarded latch would show up.
		for range c.writers {
			//: the value is irrelevant here — the race detector is the assertion.
			if err := s.copyError(); err != nil {
				swallowErr(err)
			}
		}
		wg.Wait()

		//: at least one writer failed, so a failure must be latched.
		if s.copyError() == nil {
			t.Error("copyError() = nil after a failing copier")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_outName pins the stream naming used in diagnostics.
func Test_outName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		idx  int
		want string
	}
	tests := []tc{
		{"the primary output", fdStdout, "stdout"},
		{"the diagnostic output", fdStderr, "stderr"},
		//: anything else is treated as stderr rather than producing an empty
		//: name a reader could not act on.
		{"an unexpected index", 42, "stderr"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := outName(c.idx); got != c.want {
			t.Errorf("outName(%d) = %q, want %q", c.idx, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_openNull pins that the null device opens for both directions and that a
// failure arrives typed. Every discarded stream goes through here, so a silent
// nil would leave the child with no descriptor at all on that fd.
func Test_openNull(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		flag int
	}
	tests := []tc{
		{"read-only, for a discarded stdin", os.O_RDONLY},
		{"write-only, for a discarded output", os.O_WRONLY},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		f, err := openNull(c.flag)
		if err != nil {
			t.Fatalf("openNull(%d) = %v, want nil", c.flag, err)
		}
		if f == nil {
			t.Fatal("openNull returned no file and no error")
		}
		defer func() { swallowErr(f.Close()) }()

		//: a read-only null device reads EOF immediately, which is what gives
		//: a discarded stdin its empty input.
		if c.flag == os.O_RDONLY {
			n, rerr := f.Read(make([]byte, 1))
			if n != 0 || !errors.Is(rerr, io.EOF) {
				t.Errorf("reading the null device = (%d, %v), want (0, EOF)", n, rerr)
			}
			return
		}
		//: a write-only null device accepts everything and keeps nothing.
		if _, werr := f.Write([]byte("discarded")); werr != nil {
			t.Errorf("writing to the null device = %v, want nil", werr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_wireNull pins the bookkeeping every wiring step shares: the
// descriptor lands on the child's fd, it is recorded for cleanup, and it is
// recorded as a child-side end the parent closes after the spawn.
//
// The last one is what makes EOF work. If the parent kept its copy of the
// child's write end, the read end would never see EOF and the drain would hang
// forever after the child exited.
func Test_stdioState_wireNull(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		idx  int
		flag int
	}
	tests := []tc{
		{"the input stream", fdStdin, os.O_RDONLY},
		{"the primary output", fdStdout, os.O_WRONLY},
		{"the diagnostic output", fdStderr, os.O_WRONLY},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := &stdioState{}
		defer s.closeAll()

		if err := s.wireNull(c.idx, c.flag); err != nil {
			t.Fatalf("wireNull(%s) = %v, want nil", c.name, err)
		}

		if s.files[c.idx] == nil {
			t.Fatalf("fd %d was left unwired", c.idx)
		}
		//: recorded for cleanup, or a failed spawn leaks it.
		if len(s.opened) != 1 || s.opened[0] != s.files[c.idx] {
			t.Errorf("the descriptor was not recorded for cleanup: %v", s.opened)
		}
		//: recorded as a child-side end, or the parent's copy keeps the pipe
		//: alive and the drain never reaches EOF.
		if len(s.closeChild) != 1 || s.closeChild[0] != s.files[c.idx] {
			t.Errorf("the descriptor was not recorded as a child-side end: %v", s.closeChild)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_wireInput pins the two shapes of stdin: a nil reader gives an
// immediate EOF from the null device, and a real reader gives a pipe with a
// feeder registered.
func Test_stdioState_wireInput(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		reader   io.Reader
		wantFeed bool
	}
	tests := []tc{
		{name: "no reader", wantFeed: false},
		{name: "a string reader", reader: strings.NewReader("input"), wantFeed: true},
		{name: "an empty reader", reader: strings.NewReader(""), wantFeed: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := &stdioState{}
		defer s.closeAll()

		if err := s.wireInput(c.reader); err != nil {
			t.Fatalf("wireInput(%s) = %v, want nil", c.name, err)
		}

		if s.files[fdStdin] == nil {
			t.Fatal("stdin was left unwired")
		}
		//: a feeder is registered only when there is something to feed.
		if (s.inDst != nil) != c.wantFeed {
			t.Errorf("a feeder is registered = %v, want %v", s.inDst != nil, c.wantFeed)
		}
		if c.wantFeed && s.inSrc != c.reader {
			t.Error("the feeder was registered with a different reader")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_wireOutput pins the parallel arrays the drain depends on:
// outSrc[i] must line up with outSink[i], or a caller's stdout writer would
// receive the child's stderr.
func Test_stdioState_wireOutput(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		writers []io.Writer
		wantN   int
	}
	tests := []tc{
		{name: "a discarded stream", writers: []io.Writer{nil}, wantN: 0},
		{name: "one captured stream", writers: []io.Writer{&bytes.Buffer{}}, wantN: 1},
		{name: "both streams captured", writers: []io.Writer{&bytes.Buffer{}, &bytes.Buffer{}}, wantN: 2},
		{name: "one captured, one discarded", writers: []io.Writer{&bytes.Buffer{}, nil}, wantN: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := &stdioState{}
		defer s.closeAll()

		for i, w := range c.writers {
			if err := s.wireOutput(fdStdout+i, w); err != nil {
				t.Fatalf("wireOutput(%d) = %v, want nil", fdStdout+i, err)
			}
		}

		//: the two arrays are walked by index in lock step.
		if len(s.outSrc) != len(s.outSink) {
			t.Fatalf("outSrc has %d entries and outSink %d", len(s.outSrc), len(s.outSink))
		}
		if len(s.outSrc) != c.wantN {
			t.Errorf("%d drains registered, want %d", len(s.outSrc), c.wantN)
		}
		//: each registered sink is the caller's, in the order they were wired.
		var want []io.Writer
		for _, w := range c.writers {
			if w != nil {
				want = append(want, w)
			}
		}
		for i, w := range want {
			if s.outSink[i] != w {
				t.Errorf("sink %d is not the writer it was wired with", i)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_build pins the three-stream assembly, including that a nil
// member means "discard THIS one" rather than "discard everything".
func Test_stdioState_build(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		in         io.Reader
		out        io.Writer
		errw       io.Writer
		wantDrains int
		wantFeed   bool
	}
	tests := []tc{
		{name: "everything discarded"},
		{name: "only stdout captured", out: &bytes.Buffer{}, wantDrains: 1},
		{name: "both outputs captured", out: &bytes.Buffer{}, errw: &bytes.Buffer{}, wantDrains: 2},
		{
			name:       "every stream wired",
			in:         strings.NewReader("x"),
			out:        &bytes.Buffer{},
			errw:       &bytes.Buffer{},
			wantDrains: 2,
			wantFeed:   true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := &stdioState{}
		defer s.closeAll()

		if err := s.build(c.in, c.out, c.errw); err != nil {
			t.Fatalf("build(%s) = %v, want nil", c.name, err)
		}

		//: all three child fds must be wired, whatever the caller supplied.
		for idx := range stdioFdCount {
			if s.files[idx] == nil {
				t.Errorf("fd %d was left unwired", idx)
			}
		}
		if len(s.outSrc) != c.wantDrains {
			t.Errorf("%d drains registered, want %d", len(s.outSrc), c.wantDrains)
		}
		if (s.inDst != nil) != c.wantFeed {
			t.Errorf("a feeder is registered = %v, want %v", s.inDst != nil, c.wantFeed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_buildStdio pins the mode dispatch, and the one asymmetry in it: inherit
// mode hands over os.Std* directly, so closeAll must have nothing to close —
// closing the supervisor's own stdout would be catastrophic and permanent.
func Test_buildStdio(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		spec       coreproc.Spec
		wantOpened int
		wantStdout *os.File
	}
	tests := []tc{
		{
			name:       "inherit shares the parent's streams",
			spec:       coreproc.Spec{Stdio: coreproc.StdioInherit},
			wantOpened: 0,
			wantStdout: os.Stdout,
		},
		{
			//: an unknown mode falls through to inherit rather than leaving the
			//: child with no descriptors at all.
			name:       "an unknown mode inherits too",
			spec:       coreproc.Spec{Stdio: coreproc.StdioMode(99)},
			wantOpened: 0,
			wantStdout: os.Stdout,
		},
		{
			name:       "null opens three descriptors",
			spec:       coreproc.Spec{Stdio: coreproc.StdioNull},
			wantOpened: 3,
		},
		{
			name: "capture with no writers opens three null descriptors",
			spec: coreproc.Spec{Stdio: coreproc.StdioCapture},
			//: three null devices, one per stream.
			wantOpened: 3,
		},
		{
			name: "capture with both outputs opens two pipes and a null stdin",
			spec: coreproc.Spec{
				Stdio:  coreproc.StdioCapture,
				Stdout: &bytes.Buffer{},
				Stderr: &bytes.Buffer{},
			},
			//: one null (stdin) plus two pipes, two descriptors each.
			wantOpened: 5,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s, err := buildStdio(c.spec)
		if err != nil {
			t.Fatalf("buildStdio(%s) = %v, want nil", c.name, err)
		}
		defer s.closeAll()

		for idx := range stdioFdCount {
			if s.files[idx] == nil {
				t.Errorf("fd %d was left unwired", idx)
			}
		}
		if len(s.opened) != c.wantOpened {
			t.Errorf("%d descriptors opened, want %d", len(s.opened), c.wantOpened)
		}
		//: inherit mode must never record os.Std* for closing.
		if c.wantStdout != nil {
			if s.files[fdStdout] != c.wantStdout {
				t.Errorf("stdout is not the parent's stream")
			}
			for _, f := range s.opened {
				if f == os.Stdout || f == os.Stderr || f == os.Stdin {
					t.Fatal("buildStdio recorded a parent std stream for closing")
				}
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_closeAll pins the failure-path cleanup. A spawn that aborts
// after wiring must leak nothing, and closeAll runs before any copier starts —
// so it can close freely without racing a drain.
func Test_stdioState_closeAll(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		spec coreproc.Spec
	}
	tests := []tc{
		{"a null state", coreproc.Spec{Stdio: coreproc.StdioNull}},
		{"a capture state", coreproc.Spec{Stdio: coreproc.StdioCapture, Stdout: &bytes.Buffer{}}},
		{"an inherit state", coreproc.Spec{Stdio: coreproc.StdioInherit}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s, err := buildStdio(c.spec)
		if err != nil {
			t.Fatalf("buildStdio = %v, want nil", err)
		}

		s.closeAll()
		//: a second call must not panic; the failure path can reach it twice.
		s.closeAll()

		//: every recorded descriptor is closed.
		for i, f := range s.opened {
			if _, serr := f.Stat(); serr == nil {
				t.Errorf("descriptor %d is still open after closeAll", i)
			}
		}
		//: the parent's own streams are untouched, whatever the mode.
		if _, serr := os.Stdout.Stat(); serr != nil {
			t.Fatalf("closeAll closed the supervisor's stdout: %v", serr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_drainOutput pins delivery and the latch. A writer that fails
// mid-copy must be reported, because the alternative — a truncated capture and a
// clean exit status — is indistinguishable from a program that simply printed
// less than expected.
//
// Goroutine lifecycle: one drain goroutine per case, bounded by its pipe. It
// returns when the write end is closed below, and s.wait() joins it before the
// assertions, so none can outlive the case that started it.
func Test_stdioState_drainOutput(t *testing.T) {
	t.Parallel()
	writerErr := errors.New("the sink is full")

	type tc struct {
		name    string
		payload string
		sink    io.Writer
		wantErr bool
	}
	tests := []tc{
		{name: "a sink that accepts everything", payload: "hello", sink: &bytes.Buffer{}},
		{name: "an empty stream", payload: "", sink: &bytes.Buffer{}},
		{
			name:    "a sink that fails part way",
			payload: strings.Repeat("x", 4096),
			sink:    &failingWriter{accept: 8, err: writerErr},
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe = %v", err)
		}
		//: Goroutine lifecycle: one drain goroutine, bounded by the pipe — it
		//: returns when the write end below is closed, and s.wait() joins it
		//: before the assertions.
		s := &stdioState{}
		s.wg.Add(1)
		go s.drainOutput(pr, c.sink)

		if _, werr := pw.Write([]byte(c.payload)); werr != nil && !c.wantErr {
			t.Fatalf("writing the payload: %v", werr)
		}
		//: closing the write end is what the child's exit does; the drain then
		//: reaches EOF and returns.
		swallowErr(pw.Close())
		s.wait()

		if c.wantErr {
			if s.copyError() == nil {
				t.Fatal("a failing sink was not reported")
			}
			return
		}
		if s.copyError() != nil {
			t.Fatalf("copyError() = %v, want nil", s.copyError())
		}
		buf, ok := c.sink.(*bytes.Buffer)
		if !ok {
			return
		}
		//: every byte the child wrote has reached the caller.
		if buf.String() != c.payload {
			t.Errorf("the sink received %q, want %q", buf.String(), c.payload)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_feedInput pins that the child's stdin reaches EOF. The feeder
// is fire-and-forget precisely so a reader that never ends cannot block Wait —
// but it must still close the pipe when it DOES end, or the child blocks reading
// input that will never arrive.
//
// Goroutine lifecycle: two goroutines per case, both bounded by the pipe. The
// feeder ends at the reader's EOF and closes the write end; the collector then
// reads EOF and reports on a buffered channel, so it can never block on send.
func Test_stdioState_feedInput(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input string
	}
	tests := []tc{
		{"a short input", "hello"},
		{"an empty input", ""},
		{"an input larger than a pipe buffer", strings.Repeat("y", 128*1024)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe = %v", err)
		}
		defer func() { swallowErr(pr.Close()) }()

		//: Goroutine lifecycle: two goroutines, both bounded by the pipe. The
		//: feeder ends at the reader's EOF and closes the write end; the
		//: collector then reads EOF and reports on a buffered channel, so it
		//: can never block on send.
		s := &stdioState{}
		go s.feedInput(pw, strings.NewReader(c.input))

		//: read to EOF: reaching it proves the feeder closed the write end.
		got := make(chan string, 1)
		go func() {
			data, rerr := io.ReadAll(pr)
			swallowErr(rerr)
			got <- string(data)
		}()

		select {
		case data := <-got:
			if data != c.input {
				t.Errorf("the child read %d bytes, want %d", len(data), len(c.input))
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the child's stdin never reached EOF — the feeder did not close the pipe")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_afterStart pins the post-spawn step: the parent drops its
// copies of the child-side ends and launches the copiers.
//
// Dropping those copies is what makes EOF propagate. While the parent holds the
// write end of a capture pipe, the read end never sees EOF — so the drain never
// returns, Wait never joins it, and a caller who did everything right hangs.
func Test_stdioState_afterStart(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		spec coreproc.Spec
	}
	tests := []tc{
		{"a null state has no copiers", coreproc.Spec{Stdio: coreproc.StdioNull}},
		{
			"a capture state with one output",
			coreproc.Spec{Stdio: coreproc.StdioCapture, Stdout: &bytes.Buffer{}},
		},
		{
			"a capture state with input and both outputs",
			coreproc.Spec{
				Stdio: coreproc.StdioCapture, Stdin: strings.NewReader("x"),
				Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s, err := buildStdio(c.spec)
		if err != nil {
			t.Fatalf("buildStdio = %v, want nil", err)
		}
		//: stand in for the child: it holds its own dups, and the parent drops
		//: its copies. Here nobody else holds them, so the pipes close outright
		//: and every drain reaches EOF at once.
		s.afterStart()

		done := make(chan struct{})
		go func() {
			s.wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("a copier never finished — the parent kept a child-side end open")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_stdioState_wait pins the join. Wait returns only after every byte has
// reached the caller's writers, which is the difference between "the process
// exited" and "you have all of its output".
//
// Goroutine lifecycle: two goroutines per drain, both bounded by their own pipe.
// The writer closes its end after the payload, which ends the drain; s.wait()
// joins every drain before the assertions.
func Test_stdioState_wait(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		drains  int
		payload string
	}
	tests := []tc{
		{name: "no copiers at all"},
		{name: "one copier", drains: 1, payload: "hello"},
		{name: "two copiers", drains: 2, payload: strings.Repeat("z", 64*1024)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: Goroutine lifecycle: two goroutines per drain, both bounded by their
		//: own pipe. The writer closes its end after the payload, which ends
		//: the drain; s.wait() joins every drain before the assertions.
		s := &stdioState{}
		sinks := make([]*bytes.Buffer, c.drains)
		for i := range c.drains {
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe = %v", err)
			}
			sinks[i] = &bytes.Buffer{}
			s.wg.Add(1)
			go s.drainOutput(pr, sinks[i])
			go func(w *os.File) {
				swallowErr(writeAll(w, c.payload))
				swallowErr(w.Close())
			}(pw)
		}

		s.wait()

		//: after the join every byte is in the caller's buffer, with no
		//: further synchronisation needed.
		for i, sink := range sinks {
			if sink.String() != c.payload {
				t.Errorf("sink %d holds %d bytes, want %d", i, sink.Len(), len(c.payload))
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// writeAll writes the whole string to f.
func writeAll(f *os.File, s string) error {
	_, err := io.Copy(f, strings.NewReader(s))
	return err
}

// Test_stdioState_wireInputFailure pins that a wiring failure is typed. Every
// caller of buildStdio treats the error as fatal to the spawn, so an untyped one
// would surface as "could not start" with no indication that the problem was a
// descriptor limit rather than a missing binary.
func Test_stdioState_wireInputFailure(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the stream whose wiring failure is simulated by exhausting nothing —
		//: instead the typed shape is asserted directly through wrapSpawn.
		stream string
	}
	tests := []tc{
		{"the input stream", "stdin"},
		{"the primary output", "stdout"},
		{"the null device", "null"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := wrapSpawn(os.ErrPermission, stdioField(c.stream))
		if !errs.HasCode(err, coreproc.CodeSpawnFailed) {
			t.Fatalf("a stdio wiring failure = %v, want SPAWN_FAILED", err)
		}
		var named bool
		for _, f := range errs.FieldsOf(err) {
			if f.Key() == "stdio" && f.StringValue() == c.stream {
				named = true
			}
		}
		if !named {
			t.Errorf("the failure does not name the stream: %v", errs.FieldsOf(err))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
