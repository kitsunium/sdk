package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func mustOpenSink(tb testing.TB) (sink *fileSink, path string) {
	tb.Helper()
	path = filepath.Join(tb.TempDir(), "sink.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, defaultFilePerm)
	if err != nil {
		tb.Fatalf("OpenFile err = %v", err)
	}
	//: defensive defer satisfies the lifecycle linter even though the sink
	//: takes ownership on the success path.
	defer func() {
		//: skip the close on the success path — sink owns the descriptor.
		if sink != nil {
			//: nothing to do; tb.Cleanup releases it after the test.
			return
		}
		//: factory failed downstream — release the descriptor explicitly.
		swallowSinkClose(f.Close())
	}()
	sink = &fileSink{f: f}
	//: register a cleanup so the descriptor is released even on test failure.
	tb.Cleanup(func() {
		//: best-effort close; ignore the error in cleanup to keep the test signal clean.
		swallowSinkClose(sink.Close())
	})
	return sink, path
}

// swallowSinkClose is the test-only counterpart of swallowHandlerError —
// it documents the intent of dropping a Close error from cleanup paths.
func swallowSinkClose(err error) {
	//: the test is already past its assertion phase; close errors are noise.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

func Test_fileSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		nilCtx    bool
		ctxCancel bool
		closed    bool
		wantBytes int
		wantErr   bool
		//: wantCode pins the dotted-quad identity so a refactor that swaps the
		//: sentinel (e.g. CtxCancelled vs WriteFailed) is caught, not just "an error".
		wantCode errs.Code
	}{
		{"happy path writes bytes", false, false, false, 5, false, 0},
		{"nil ctx is treated as live and writes", true, false, false, 5, false, 0},
		{"cancelled context returns 0 bytes", false, true, false, 0, true, CodeCtxCancelled},
		{"closed file surfaces WriteFailed", false, false, true, 0, true, CodeWriteFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := mustOpenSink(t)
			if tc.closed {
				//: pre-close so Write hits the underlying *os.File error path.
				swallowSinkClose(s.Close())
			}
			var ctx context.Context
			if tc.nilCtx {
				ctx = nil
			} else {
				ctx = t.Context()
				if tc.ctxCancel {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				}
			}
			n, err := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("hello"))
			if n != tc.wantBytes {
				t.Errorf("Write n = %d, want %d", n, tc.wantBytes)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("Write err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantCode != 0 && !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
			if tc.ctxCancel && !errors.Is(err, context.Canceled) {
				//: the cancel path wraps ctx.Err() so stdlib Is() keeps working.
				t.Errorf("errors.Is(%v, context.Canceled) = false", err)
			}
		})
	}
}

func Test_fileSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		nilCtx    bool
		ctxCancel bool
		closed    bool
		wantErr   bool
		//: wantCode pins the SyncFailed identity on the closed-file path; the
		//: cancelled-ctx path intentionally returns ctx.Err() unwrapped (code 0).
		wantCode errs.Code
	}{
		{"flush with live ctx is nil", false, false, false, false, 0},
		{"flush with nil ctx is nil", true, false, false, false, 0},
		{"flush with cancelled ctx surfaces error", false, true, false, true, 0},
		{"flush on closed file surfaces SyncFailed", false, false, true, true, CodeSyncFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := mustOpenSink(t)
			if tc.closed {
				//: pre-close so Sync hits the underlying *os.File error path.
				swallowSinkClose(s.Close())
			}
			var ctx context.Context
			if tc.nilCtx {
				ctx = nil
			} else {
				ctx = t.Context()
				if tc.ctxCancel {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				}
			}
			err := s.Flush(ctx)
			if (err != nil) != tc.wantErr {
				t.Errorf("Flush err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantCode != 0 && !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
			if tc.ctxCancel && !errors.Is(err, context.Canceled) {
				//: Flush returns ctx.Err() directly (no CtxCancelled wrap), so
				//: only the stdlib Is() guarantee holds on the cancel path.
				t.Errorf("errors.Is(%v, context.Canceled) = false", err)
			}
		})
	}
}

func Test_fileSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: doubleClose drives a second Close so the second call hits the
		//: kernel's "file already closed" error wrapped as CloseFailed.
		doubleClose bool
		wantCode    errs.Code
	}{
		{"Close releases the descriptor without error", false, 0},
		{"double close returns CloseFailed", true, CodeCloseFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := mustOpenSink(t)
			if err := s.Close(); err != nil {
				t.Errorf("first Close err = %v", err)
			}
			if !tc.doubleClose {
				//: single-close case asserted above; nothing further to do.
				return
			}
			err := s.Close()
			if !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false on second Close", err, tc.wantCode)
			}
		})
	}
}

func Test_fileSink_WriteAfterClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"write after close surfaces WriteFailed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := mustOpenSink(t)
			//: close first so the subsequent Write hits a dead descriptor — the
			//: kernel "file already closed" error must surface as WriteFailed.
			swallowSinkClose(s.Close())
			_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("after"))
			if !errs.HasCode(err, CodeWriteFailed) {
				t.Errorf("HasCode(%v, CodeWriteFailed) = false", err)
			}
		})
	}
}

// Test_fileSink_ConcurrentWrite spawns concurrent goroutines that each call
// Write once; every goroutine is joined via wg.Wait before the assertion, so
// none outlives the test. The -race flag is the real oracle for the Write mutex.
func Test_fileSink_ConcurrentWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: writers is the goroutine fan-out exercising the Write mutex; -race
		//: flags any data race on s.f or the byte accounting.
		writers int
	}{
		{"8 concurrent writers do not race", 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := mustOpenSink(t)
			payload := []byte("payload\n")
			var wg sync.WaitGroup
			//: a dedicated mutex guards the test's own accumulator — the sink's
			//: internal mutex protects the file, not this counter.
			var mu sync.Mutex
			total := 0
			wg.Add(tc.writers)
			//: fan out the writers; wg.Wait below joins them before asserting.
			for range tc.writers {
				go func() {
					defer wg.Done()
					n, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, payload)
					if err != nil {
						//: a write failure under concurrency is a hard test failure.
						t.Errorf("concurrent Write err = %v", err)
						return
					}
					mu.Lock()
					total += n
					mu.Unlock()
				}()
			}
			wg.Wait()
			//: every writer emitted the full payload exactly once.
			if want := tc.writers * len(payload); total != want {
				t.Errorf("total bytes = %d, want %d", total, want)
			}
		})
	}
}

// symlinkRefusalCase is one refusal under test: which of the two enforcement
// points produced it, against which path, and whether kind=symlink is expected.
type symlinkRefusalCase struct {
	// name labels the subtest.
	name string
	// path is the file the refusal is asked about.
	path string
	// refuse is the refusal under test, already bound to its shape.
	refuse func(path string) error
	// wantKind says whether kind=symlink must be present.
	wantKind bool
}

// fieldValue reads one errs field off err, reporting whether it was there at
// all — "absent" and "present but empty" are different claims and the caller
// asserts on the difference.
func fieldValue(t *testing.T, err error, key string) (value string, present bool) {
	t.Helper()
	var typed *errs.Error
	//: every refusal in this package is an *errs.Error; anything else means
	//: the wrap was lost and the fields with it.
	if !errors.As(err, &typed) {
		t.Fatalf("err %v is not an *errs.Error", err)
	}
	for _, f := range typed.Fields() {
		//: first match wins; the package never sets a key twice.
		if f.Key() == key {
			return f.StringValue(), true
		}
	}
	return "", false
}

// TestBothSymlinkRefusalsNameTheIndirection pins the field that makes a planted
// link legible. CodeOpenFailed covers every open failure in this package, so
// without the field a full disk and someone redirecting the log path produce
// the same line in the operator's log. The policy refusal (refuseSymlink,
// before the open) and the kernel refusal (explainOpenFailure, after it) must
// agree on the spelling — a consumer filtering on kind=symlink that saw only
// one of the two would miss the common case or the attack case depending on
// which one it got.
func TestBothSymlinkRefusalsNameTheIndirection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(dir, "planted.log")
	regular := filepath.Join(dir, "ordinary.log")
	//: the two refusals need a real link and a real non-link to look at; an
	//: unplantable link leaves the field unverified, so fail rather than skip —
	//: the stance TestNew_RejectsSymlink already takes in this package.
	if lerr := os.Symlink(filepath.Join(dir, "elsewhere"), link); lerr != nil {
		t.Fatalf("plant symlink: %v", lerr)
	}
	//: the third row needs a path that is a file and not a link; a seed that
	//: failed would leave it absent, and "absent" is a state refuseSymlink
	//: passes through — the row would then assert nothing about an ordinary
	//: failure.
	if werr := os.WriteFile(regular, []byte("x"), defaultFilePerm); werr != nil {
		t.Fatalf("seed regular: %v", werr)
	}
	//: explainOpenFailure's cause is a stdlib sentinel rather than a fresh
	//: error so the row asserts the wrap, not an error value it invented.
	fromKernel := func(path string) error { return explainOpenFailure(path, os.ErrPermission) }
	tests := []symlinkRefusalCase{
		{"the check before the open names it", link, refuseSymlink, true},
		{"the kernel refusal after the open names it", link, fromKernel, true},
		{"an ordinary open failure does not", regular, fromKernel, false},
	}
	runCase := func(t *testing.T, c symlinkRefusalCase) {
		t.Helper()
		err := c.refuse(c.path)
		//: every arm is the same sentinel — only the fields separate them.
		if !errs.HasCode(err, CodeOpenFailed) {
			t.Fatalf("%s: err=%v want open-failed", c.name, err)
		}
		//: the path is always named, so an operator can find the file.
		if got, ok := fieldValue(t, err, "path"); !ok || got != c.path {
			t.Errorf("%s: path field = %q (present=%v), want %q", c.name, got, ok, c.path)
		}
		got, ok := fieldValue(t, err, "kind")
		//: absent is the assertion on the ordinary arm, not empty-string.
		if ok != c.wantKind || (c.wantKind && got != kindSymlink) {
			t.Errorf("%s: kind field = %q (present=%v), want present=%v value=%q",
				c.name, got, ok, c.wantKind, kindSymlink)
		}
	}
	//: parallel, unlike the O_NOFOLLOW table: these rows share no counter and
	//: touch no state beyond the two fixtures seeded above, which are read and
	//: never written by a row.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
