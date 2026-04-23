package file_test

import (
	"context"
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

// TestNew_RejectsSymlink regresses finding #3 — a pre-planted symlink at
// the destination path would otherwise cause os.OpenFile to follow it and
// redirect writes to an attacker-chosen target. file.New must reject the
// symlink via Lstat + CodeOpenFailed.
func TestNew_RejectsSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.log")
	link := filepath.Join(dir, "attacker.log")
	//: create a harmless target so the symlink resolves; the hardening
	//: policy rejects the symlink regardless of whether the target is
	//: safe — we never follow it.
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
		s.Close()
	}
	if !errs.HasCode(err, file.CodeOpenFailed) {
		t.Errorf("HasCode(err, CodeOpenFailed) = false; err = %v", err)
	}
}

// TestNew_DefaultFilePermIs0600 regresses finding #3 — freshly-created log
// files must be owner-read/write only. World-readable logs (0644) would
// leak diagnostic content including attr values and wrapped Private
// fields that consumers may include in their own log lines.
func TestNew_DefaultFilePermIs0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "perm.log")
	s, err := file.New(path)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	defer func() { s.Close() }()
	fi, serr := os.Stat(path)
	if serr != nil {
		t.Fatalf("Stat err = %v", serr)
	}
	//: 0600 = owner rw only; any group/other bit indicates a regression.
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("file perm = %o, want 0600", got)
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

func TestFileSinkSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"PathEmpty carries 3501", file.PathEmpty, file.CodePathEmpty},
		{"OpenFailed carries 3502", file.OpenFailed, file.CodeOpenFailed},
		{"CtxCancelled carries 3510", file.CtxCancelled, file.CodeCtxCancelled},
		{"WriteFailed carries 3520", file.WriteFailed, file.CodeWriteFailed},
		{"SyncFailed carries 3530", file.SyncFailed, file.CodeSyncFailed},
		{"CloseFailed carries 3540", file.CloseFailed, file.CodeCloseFailed},
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
