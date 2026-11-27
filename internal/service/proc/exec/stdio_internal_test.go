// Package exec — the stdio wiring. Every function here manages a descriptor or a
// goroutine, and the failure modes are the two that are hardest to see: output
// that is silently truncated, and a copier that outlives the process it was
// draining.
package exec

import (
	"bytes"
	"errors"
	"fmt"
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

// Test_stdioState_wireNull pins the null stream's bookkeeping since #943: the
// descriptor lands on the child's fd, and it is the shared null device, on no
// cleanup list — closing it would take it from every later spawn. (A pipe's
// child-side end is still closed after the spawn, which is what makes EOF
// work; the null device has no reader waiting for one.)
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

		if err := s.wireNull(c.idx); err != nil {
			t.Fatalf("wireNull(%s) = %v, want nil", c.name, err)
		}

		if s.files[c.idx] == nil {
			t.Fatalf("fd %d was left unwired", c.idx)
		}
		//: the shared null device, kept open for the next spawn.
		if s.files[c.idx] != sharedNull.Load() {
			t.Errorf("fd %d is not the shared null device", c.idx)
		}
		if len(s.opened) != 0 || len(s.closeChild) != 0 {
			t.Errorf("the shared null device is on a cleanup list: %v, %v", s.opened, s.closeChild)
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
			name:       "null opens nothing: the shared null device serves all three (#943)",
			spec:       coreproc.Spec{Stdio: coreproc.StdioNull},
			wantOpened: 0,
		},
		{
			name: "capture with no writers opens nothing: the shared null device",
			spec: coreproc.Spec{Stdio: coreproc.StdioCapture},
			//: three null devices, one per stream.
			wantOpened: 0,
		},
		{
			name: "capture with both outputs opens two pipes, the stdin is the shared null device",
			spec: coreproc.Spec{
				Stdio:  coreproc.StdioCapture,
				Stdout: &bytes.Buffer{},
				Stderr: &bytes.Buffer{},
			},
			//: one null (stdin) plus two pipes, two descriptors each.
			wantOpened: 4,
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
// returns when the write end is closed below, and s.wg.Wait() joins it before the
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
		//: returns when the write end below is closed, and s.wg.Wait() joins it
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
		s.wg.Wait()

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
			s.wg.Wait()
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
		name   string
		drains int
		size   int
	}
	tests := []tc{
		{name: "no copiers at all"},
		{name: "one copier", drains: 1, size: 5},
		{name: "two copiers", drains: 2, size: 64 * 1024},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: Goroutine lifecycle: two goroutines per drain, both bounded by their
		//: own pipe. The writer closes its end after the payload, which ends
		//: the drain; s.wg.Wait() joins every drain before the assertions.
		s := &stdioState{}
		sinks := make([]*bytes.Buffer, c.drains)
		//: each writer reports on its own buffered channel, read after the
		//: join: a failed write is named next to the sink it starved (#1501).
		wrote := make([]chan error, c.drains)
		//: every drain reads through a log of its reads, and every pipe
		//: carries its own tagged payload, so a wrong byte names its pipe,
		//: its offset and the read that returned it (#1501).
		logs := make([]*readLog, c.drains)
		payloads := make([]string, c.drains)
		for i := range c.drains {
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe = %v", err)
			}
			sinks[i] = &bytes.Buffer{}
			wrote[i] = make(chan error, 1)
			logs[i] = &readLog{src: pr}
			payloads[i] = taggedPayload(i, c.size)
			s.wg.Add(1)
			go s.drainOutput(logs[i], sinks[i])
			go func(w *os.File, payload string, done chan<- error) {
				werr := writeAll(w, payload)
				done <- errors.Join(werr, w.Close())
			}(pw, payloads[i], wrote[i])
		}

		s.wg.Wait()

		//: after the join every byte is in the caller's buffer, and every
		//: read is in its log, with no further synchronisation needed.
		for i, sink := range sinks {
			werr := <-wrote[i]
			//: a sink that is not its pipe's payload, byte for byte, fails.
			if got := sink.String(); got != payloads[i] {
				t.Errorf("sink %d (writer error %v): %s; %s", i, werr,
					describeMismatch(got, payloads[i]), logs[i].around(got, payloads[i]))
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

// mismatchWindow is how many bytes describeMismatch quotes from the first
// difference on.
const mismatchWindow int = 16

// describeMismatch says how got differs from want: both lengths, the first
// offset that differs with the bytes found and expected from there, how many
// of the common bytes differ, and the last one. Equal lengths with the wrong
// content is what #1501 hid behind "holds 65536 bytes, want 65536". The
// bytes found name their source — another test's pipe writes 'x' or 'y',
// lost memory reads as zeros — and the offsets — a whole page, a 16- or
// 32-byte lane, a scattered few — tell a kernel's page handling from a
// register lost across a signal.
func describeMismatch(got, want string) string {
	n := min(len(got), len(want))
	first, last, differ := -1, -1, 0
	for i := range n {
		//: equal bytes say nothing.
		if got[i] == want[i] {
			continue
		}
		//: the first difference is the one quoted.
		if first < 0 {
			first = i
		}
		last = i
		differ++
	}
	//: one is a prefix of the other: the first difference is where it ends.
	if first < 0 {
		first = n
	}
	return fmt.Sprintf("holds %d bytes, want %d; first difference at byte %d (page offset %d): found %q, want %q; %d of the %d common bytes differ, the last at byte %d",
		len(got), len(want), first, first%4096, mismatchAt(got, first), mismatchAt(want, first), differ, n, last)
}

// Test_describeMismatch pins the message Test_stdioState_wait fails with: it
// names the first differing byte, what was found and expected there, and how
// far the damage reaches, so one failed run on a platform nobody has at hand
// is enough to tell the causes apart (#1501).
func Test_describeMismatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		got, want string
		message   string
	}
	tests := []tc{
		{
			name: "equal lengths, one page zeroed",
			got:  strings.Repeat("z", 4096) + strings.Repeat("\x00", 4096), want: strings.Repeat("z", 8192),
			message: `holds 8192 bytes, want 8192; first difference at byte 4096 (page offset 0): ` +
				`found "\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00", want "zzzzzzzzzzzzzzzz"; ` +
				`4096 of the 8192 common bytes differ, the last at byte 8191`,
		},
		{
			name: "another pipe's bytes in the middle",
			got:  "zzzyyzz", want: "zzzzzzz",
			message: `holds 7 bytes, want 7; first difference at byte 3 (page offset 3): found "yyzz", want "zzzz"; ` +
				`2 of the 7 common bytes differ, the last at byte 4`,
		},
		{
			name: "a short sink",
			got:  "zz", want: "zzz",
			message: `holds 2 bytes, want 3; first difference at byte 2 (page offset 2): found "", want "z"; ` +
				`0 of the 2 common bytes differ, the last at byte -1`,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := describeMismatch(c.got, c.want); got != c.message {
			t.Errorf("describeMismatch =\n%s\nwant\n%s", got, c.message)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// mismatchAt is the window of s describeMismatch quotes from offset first.
func mismatchAt(s string, first int) string {
	end := min(len(s), first+mismatchWindow)
	//: s ends at or before the difference.
	if first >= end {
		return ""
	}
	return s[first:end]
}

// recordSize is the length of one record of taggedPayload: a letter naming
// the pipe, then the record's offset in seven hexadecimal digits.
const recordSize int = 8

// taggedPayload is size bytes of 8-byte records, each the letter 'A'+pipe and
// its own offset in hexadecimal ("A0000000A0000008…"), cut at size. A byte
// that is not its pipe's then says where it came from: another pipe of the
// case (its letter), an earlier or later part of the same stream (its offset),
// another test's pipe (that test's bytes), or lost memory (zeros) (#1501).
func taggedPayload(pipe, size int) string {
	var b strings.Builder
	b.Grow(size + recordSize)
	//: one record per recordSize bytes, the last one cut at size.
	for off := 0; off < size; off += recordSize {
		fmt.Fprintf(&b, "%c%07x", 'A'+pipe, off)
	}
	return b.String()[:size]
}

// readSpan is one Read the drain made: where it starts in the stream and how
// many bytes it returned.
type readSpan struct {
	off, n int
}

// readLog is a pipe's read end that records every Read. Only the drain reads
// it, and the test reads the log after the drain's join.
type readLog struct {
	src   *os.File
	next  int
	reads []readSpan
}

// Read reads from the pipe and records the span it returned.
func (r *readLog) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	//: an empty read moves nothing.
	if n > 0 {
		r.reads = append(r.reads, readSpan{off: r.next, n: n})
		r.next += n
	}
	return n, err
}

// Close closes the pipe's read end.
func (r *readLog) Close() error {
	return r.src.Close()
}

// around names the reads that returned the first and the last byte where got
// differs from want, and how many reads there were: a difference inside one
// read's bounds, or covering whole reads, points at what the kernel returned;
// one that crosses reads at 16- or 32-byte lanes points at a copy in this
// process (#1501).
func (r *readLog) around(got, want string) string {
	n := min(len(got), len(want))
	first, last := -1, -1
	for i := range n {
		//: equal bytes say nothing.
		if got[i] == want[i] {
			continue
		}
		//: the first difference is kept.
		if first < 0 {
			first = i
		}
		last = i
	}
	//: no common byte differs: the sink is short or long, not wrong.
	if first < 0 {
		return fmt.Sprintf("%d reads, %d bytes", len(r.reads), r.next)
	}
	return fmt.Sprintf("%d reads, %d bytes; byte %d came from read %s, byte %d from read %s",
		len(r.reads), r.next, first, r.spanOf(first), last, r.spanOf(last))
}

// spanOf names the read that returned the stream's byte at off.
func (r *readLog) spanOf(off int) string {
	//: the reads are in stream order, without gaps.
	for i, s := range r.reads {
		//: off lies within this read.
		if off >= s.off && off < s.off+s.n {
			return fmt.Sprintf("#%d [%d, %d)", i, s.off, s.off+s.n)
		}
	}
	return "none"
}

// Test_readLog pins what Test_stdioState_wait reports beside the mismatch: a
// tagged payload's records, and the reads that returned the first and the
// last wrong byte (#1501).
func Test_readLog(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		reads     []readSpan
		got, want string
		message   string
	}
	want := taggedPayload(1, 20)
	tests := []tc{
		{
			name: "the tagged records", reads: []readSpan{{0, 20}}, got: want, want: want,
			message: "B0000000B0000008B000|1 reads, 20 bytes",
		},
		{
			name:  "another pipe's record in the second read",
			reads: []readSpan{{0, 8}, {8, 12}},
			got:   want[:8] + "A0000008" + want[16:], want: want,
			message: "B0000000B0000008B000|2 reads, 20 bytes; " +
				"byte 8 came from read #1 [8, 20), byte 8 from read #1 [8, 20)",
		},
		{
			name:  "the damage crosses two reads",
			reads: []readSpan{{0, 10}, {10, 10}},
			got:   want[:6] + "\x00\x00\x00\x00\x00\x00" + want[12:], want: want,
			message: "B0000000B0000008B000|2 reads, 20 bytes; " +
				"byte 6 came from read #0 [0, 10), byte 11 from read #1 [10, 20)",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &readLog{reads: c.reads}
		//: the log's total is the sum of its reads.
		for _, s := range c.reads {
			r.next += s.n
		}
		if got := want + "|" + r.around(c.got, c.want); got != c.message {
			t.Errorf("payload|around =\n%s\nwant\n%s", got, c.message)
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
