package file_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/sink/file"
)

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		path     string
		wantCode errs.Code
	}{
		{"empty path is rejected", "", file.CodePathEmpty},
		{"valid path opens the file", filepath.Join(t.TempDir(), "ok.log"), 0},
		{"invalid directory yields OpenFailed", "/this/dir/does/not/exist/file.log", file.CodeOpenFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := file.New(tc.path)
			if tc.wantCode == 0 {
				if err != nil {
					t.Errorf("New err = %v, want nil", err)
				}
				if s == nil {
					t.Error("New returned nil sink on happy path")
				} else if cerr := s.Close(); cerr != nil {
					t.Errorf("Close err = %v", cerr)
				}
				return
			}
			if !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

// TestNew_RejectsSymlink — a pre-planted symlink at
// the destination path would otherwise cause os.OpenFile to follow it and
// redirect writes to an attacker-chosen target. file.New must reject the
// symlink via Lstat + CodeOpenFailed.
func TestNew_RejectsSymlink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: targetSuffix names the harmless backing file the symlink points to.
		targetSuffix string
		//: linkSuffix names the symlink itself; file.New must refuse to open it.
		linkSuffix string
	}{
		{"basic symlink in temp dir", "target.log", "attacker.log"},
		{"alternate filename combinations", "real.log", "evil.log"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			target := filepath.Join(dir, tc.targetSuffix)
			link := filepath.Join(dir, tc.linkSuffix)
			//: create a harmless target so the symlink resolves; the
			//: hardening policy rejects the symlink regardless of whether
			//: the target is safe — we never follow it.
			if ferr := os.WriteFile(target, []byte("pre-existing"), 0o600); ferr != nil {
				t.Fatalf("setup write target: %v", ferr)
			}
			if lerr := os.Symlink(target, link); lerr != nil {
				t.Fatalf("setup symlink: %v", lerr)
			}
			s, err := file.New(link)
			if s != nil {
				t.Errorf("New returned a non-nil sink through a symlink")
				//: best-effort cleanup so the test never leaks descriptors.
				closeIgnore(t, s)
			}
			if !errs.HasCode(err, file.CodeOpenFailed) {
				t.Errorf("HasCode(err, CodeOpenFailed) = false; err = %v", err)
			}
		})
	}
}

// TestNew_DefaultFilePermIs0600 — freshly-created log
// files must be owner-read/write only. World-readable logs (0644) would
// leak diagnostic content including attr values and wrapped Private
// fields that consumers may include in their own log lines.
func TestNew_DefaultFilePermIs0600(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: pathSuffix names the file created under t.TempDir for the case.
		pathSuffix string
		//: wantPerm is the expected mode bits after creation.
		wantPerm os.FileMode
	}{
		{"default perm under standard suffix", "perm.log", 0o600},
		{"default perm under nested suffix", "nested.log", 0o600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), tc.pathSuffix)
			s, err := file.New(path)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			t.Cleanup(func() { closeIgnore(t, s) })
			fi, serr := os.Stat(path)
			if serr != nil {
				t.Fatalf("Stat err = %v", serr)
			}
			//: 0600 = owner rw only; any group/other bit is a regression.
			if got := fi.Mode().Perm(); got != tc.wantPerm {
				t.Errorf("file perm = %o, want %o", got, tc.wantPerm)
			}
		})
	}
}

// closeIgnore swallows the Close error in cleanup paths where the failure
// is not the assertion target. Defensive guard so err is observed by the
// audit even when the helper appears in deferred cleanup. Cleanup failures
// are surfaced through testing.TB so the test still reports anomalous
// teardown behaviour without flipping the assertion target.
func closeIgnore(tb testing.TB, s corelogger.Sink) {
	tb.Helper()
	//: touch the receiver so the unused-param audit treats this no-op as intentional.
	if s == nil {
		//: nothing to close on the happy path.
		return
	}
	//: best-effort close — caller already asserted the meaningful failure.
	if cerr := s.Close(); cerr != nil {
		//: surface cleanup anomalies via t.Log so they are not invisible.
		tb.Logf("closeIgnore: Close err = %v", cerr)
	}
}

func TestFileSink_WriteFlushClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ctxCancel bool
		payload   []byte
	}{
		{"write + flush + close on happy path", false, []byte("hello\n")},
		{"cancelled ctx surfaces error", true, []byte("dropped\n")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "log.txt")
			s, err := file.New(path)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ctx := t.Context()
			if tc.ctxCancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			rec := corelogger.RecordEvent{Level: level.Info}
			_, werr := s.Write(ctx, rec, tc.payload)
			if tc.ctxCancel {
				if werr == nil {
					t.Error("Write on cancelled ctx returned nil err, want non-nil")
				}
				return
			}
			if werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			if ferr := s.Flush(ctx); ferr != nil {
				t.Errorf("Flush err = %v", ferr)
			}
			if cerr := s.Close(); cerr != nil {
				t.Errorf("Close err = %v", cerr)
			}
			//: read back the file and confirm the payload landed verbatim.
			got, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("ReadFile err = %v", rerr)
			}
			if !strings.Contains(string(got), string(tc.payload)) {
				t.Errorf("file content = %q, want to contain %q", got, tc.payload)
			}
		})
	}
}

// TestFileSink_AppendSemantics exercises O_APPEND end-to-end: two distinct
// payloads written in sequence must land in write order, not be truncated or
// reordered by the kernel's append handling.
func TestFileSink_AppendSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		first []byte
		//: second is written after first; its bytes must follow first on disk.
		second []byte
	}{
		{"two distinct lines append in order", []byte("alpha\n"), []byte("bravo\n")},
		{"non-newline payloads still append in order", []byte("first"), []byte("second")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "append.log")
			s, err := file.New(path)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ctx := t.Context()
			rec := corelogger.RecordEvent{Level: level.Info}
			if _, werr := s.Write(ctx, rec, tc.first); werr != nil {
				t.Fatalf("first Write err = %v", werr)
			}
			if _, werr := s.Write(ctx, rec, tc.second); werr != nil {
				t.Fatalf("second Write err = %v", werr)
			}
			if ferr := s.Flush(ctx); ferr != nil {
				t.Fatalf("Flush err = %v", ferr)
			}
			if cerr := s.Close(); cerr != nil {
				t.Fatalf("Close err = %v", cerr)
			}
			got, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("ReadFile err = %v", rerr)
			}
			content := string(got)
			firstAt := strings.Index(content, string(tc.first))
			secondAt := strings.Index(content, string(tc.second))
			if firstAt < 0 || secondAt < 0 {
				t.Fatalf("content %q missing a payload (first@%d second@%d)", content, firstAt, secondAt)
			}
			//: O_APPEND guarantees the second write starts past the first.
			if firstAt >= secondAt {
				t.Errorf("append order wrong: first@%d not before second@%d", firstAt, secondAt)
			}
		})
	}
}

// TestFileSink_FlushCtxCancelledReturnsCtxErr documents the intentional
// asymmetry vs Write: Flush returns ctx.Err() unwrapped, so errors.Is holds
// but the CtxCancelled dotted-quad code is deliberately absent.
func TestFileSink_FlushCtxCancelledReturnsCtxErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"cancelled flush yields bare context.Canceled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "flush.log")
			s, err := file.New(path)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			t.Cleanup(func() { closeIgnore(t, s) })
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			ferr := s.Flush(cancelled)
			//: stdlib Is() must hold — the cancellation cause is preserved.
			if !errors.Is(ferr, context.Canceled) {
				t.Errorf("errors.Is(%v, context.Canceled) = false", ferr)
			}
			//: but Flush does NOT wrap, so the dotted-quad code is absent by design.
			if errs.HasCode(ferr, file.CodeCtxCancelled) {
				t.Errorf("HasCode(%v, CodeCtxCancelled) = true, want false (Flush returns bare ctx.Err)", ferr)
			}
		})
	}
}

// TestFileSink_WriteCtxCancelledCodeIdentity documents the Write wrap chain:
// a cancelled context surfaces BOTH the CtxCancelled dotted-quad code AND the
// stdlib context.Canceled cause via the errs.Wrap chain.
func TestFileSink_WriteCtxCancelledCodeIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"cancelled write carries code and cause"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "write.log")
			s, err := file.New(path)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			t.Cleanup(func() { closeIgnore(t, s) })
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			rec := corelogger.RecordEvent{Level: level.Info}
			_, werr := s.Write(cancelled, rec, []byte("dropped"))
			//: Write wraps ctx.Err() under CtxCancelled — both identities hold.
			if !errs.HasCode(werr, file.CodeCtxCancelled) {
				t.Errorf("HasCode(%v, CodeCtxCancelled) = false", werr)
			}
			if !errors.Is(werr, context.Canceled) {
				t.Errorf("errors.Is(%v, context.Canceled) = false", werr)
			}
		})
	}
}

func TestFileSinkSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"PathEmpty carries 0.3.14.1", file.PathEmpty, file.CodePathEmpty},
		{"OpenFailed carries 0.3.14.2", file.OpenFailed, file.CodeOpenFailed},
		{"CtxCancelled carries 0.3.14.10", file.CtxCancelled, file.CodeCtxCancelled},
		{"WriteFailed carries 0.3.14.20", file.WriteFailed, file.CodeWriteFailed},
		{"SyncFailed carries 0.3.14.30", file.SyncFailed, file.CodeSyncFailed},
		{"CloseFailed carries 0.3.14.40", file.CloseFailed, file.CodeCloseFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("HasCode(%v, %d) = false", tc.err, tc.code)
			}
		})
	}
}
