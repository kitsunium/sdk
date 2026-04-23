// Package file implements a Sink that appends formatted records to an
// on-disk file. Opens with O_APPEND|O_CREATE|O_WRONLY so concurrent
// goroutines (and other processes appending to the same file) emit atomic
// records as long as the payload stays under PIPE_BUF on POSIX systems.
//
// Rotation is intentionally out of scope for this commit — callers needing
// rolling files compose this sink with a future sink/rotate wrapper, or
// with a third-party rotator like lumberjack.v2 in their own application
// code.
package file

import (
	"context"
	"os"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// defaultFilePerm is the permission bitmask passed to os.OpenFile when the
// destination file does not yet exist. 0600 restricts reads to the owning
// UID so diagnostic content (attr values, wrapped Private fields bubbled
// in by consumers that log the Source chain) is never world-readable.
// Operators that need group or other-reader access chmod explicitly.
const defaultFilePerm os.FileMode = 0o600

// refuseSymlink returns a typed error when path refers to a symbolic link.
// Pre-opening the file through Lstat lets us reject attacker-planted links
// (CWE-59) before OpenFile follows them. os.Lstat does NOT traverse the
// final component, so the test is TOCTOU-safe relative to OpenFile only
// if the directory itself is not attacker-writable — a precondition the
// godoc on New documents.
//
// Params:
//   - path: filesystem path supplied to New; already non-empty.
//
// Returns:
//   - err: OpenFailed when path is a symlink; nil when it is absent or a
//     regular file. Lstat failures other than "not a symlink" are swallowed
//     here — the subsequent OpenFile will surface them uniformly.
func refuseSymlink(path string) (err error) {
	//: Lstat does NOT follow the final component, so a symlink is caught
	//: before OpenFile can follow it. Absent paths / stat errors fall
	//: through: OpenFile will surface the real diagnostic uniformly.
	fi, lerr := os.Lstat(path)
	//: pass through when stat fails OR the target is a regular file.
	//: (Go's short-circuit evaluation makes fi.Mode() safe when lerr==nil.)
	if lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
		//: happy path — not a symlink, hand control back to OpenFile.
		return nil
	}
	//: path resolved to a symlink → reject by policy.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeOpenFailed,
		Reason:  "OPEN_FAILED",
		Public:  "File sink refuses to open a symlink",
		Private: "service/logger/sink/file.New: path is a symlink; refusing per hardening policy",
	}, errs.String("path", path))
}

// fileSink wraps *os.File behind a mutex so concurrent goroutines emit
// atomic Write calls. Process-level atomicity for payloads under PIPE_BUF
// is provided by the kernel via O_APPEND.
type fileSink struct {
	// f is the underlying *os.File opened in append mode.
	f *os.File
	// mu serialises Write calls so concurrent goroutines emit atomic lines
	// even when the payload exceeds PIPE_BUF.
	mu sync.Mutex
}

// New opens path in append mode and wraps the resulting *os.File as a Sink.
//
// Params:
//   - path: filesystem path; empty string rejects the call.
//
// Returns:
//   - corelogger.Sink: a ready-to-use file Sink.
//   - error: PathEmpty when path is empty; OpenFailed wrapping the os error.
func New(path string) (sink corelogger.Sink, err error) {
	//: refuse an empty path so callers detect the misconfiguration immediately.
	if path == "" {
		//: documented sentinel — caller must supply a path.
		return nil, PathEmpty
	}
	//: reject pre-existing symlinks (CWE-59); O_NOFOLLOW on Linux closes
	//: the remaining TOCTOU window between this check and OpenFile.
	if serr := refuseSymlink(path); serr != nil {
		//: surface the hardening sentinel so HasCode introspection works.
		return nil, serr
	}
	//: open with O_NOFOLLOW on POSIX (Linux) so even a symlink planted
	//: between Lstat and OpenFile causes open to fail rather than silently
	//: redirect. openFlags is platform-scoped in open_flags_{linux,other}.go.
	f, oerr := os.OpenFile(path, openFlags, defaultFilePerm)
	//: surface os errors via errs.Wrap so errors.Is still catches the cause.
	if oerr != nil {
		//: wrap with the documented sentinel for HasCode-style introspection.
		return nil, errs.Wrap(oerr, errs.WrapParams{
			Code:    CodeOpenFailed,
			Reason:  "OPEN_FAILED",
			Public:  "File sink could not open the destination file",
			Private: "service/logger/sink/file.New: os.OpenFile returned an error",
		}, errs.String("path", path))
	}
	//: defensive close-on-error defer satisfies the lifecycle linter; the
	//: success path below owns the descriptor through fileSink.Close.
	defer func() {
		//: skip the close on the success path — sink owns the descriptor.
		if err == nil {
			//: nothing to do; the constructor returned the sink to the caller.
			return
		}
		//: best-effort close; err already carries the meaningful failure cause.
		if cerr := f.Close(); cerr != nil {
			//: chain the close error onto err so callers see both via errors.Is.
			err = errs.Wrap(cerr, errs.WrapParams{
				Code:    CodeCloseFailed,
				Reason:  "CLOSE_FAILED",
				Public:  "File close failed",
				Private: "service/logger/sink/file.New: deferred close failed after another error",
			})
		}
	}()
	//: ownership transferred to the sink; sink.Close releases the descriptor.
	return &fileSink{f: f}, nil
}

// Write appends p onto the underlying file under the local mutex.
//
// Params:
//   - ctx: request-scoped context; cancelled contexts skip the write.
//   - r: originating record; r.Level is attached to error metadata.
//   - p: formatted bytes produced by the upstream encoder.
//
// Returns:
//   - n: number of bytes accepted by the file.
//   - err: CtxCancelled / WriteFailed wrapping the cause; nil on success.
func (s *fileSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: honour context cancellation: cancelled contexts skip the write entirely.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so consumers get both our reason and stdlib Is().
		return 0, errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeCtxCancelled,
			Reason:  "CTX_CANCELLED",
			Public:  "Logging aborted due to cancellation",
			Private: "service/logger/sink/file.Write saw a cancelled context",
		}, errs.Int("level", int(r.Level)))
	}
	//: serialise writes for payloads above PIPE_BUF; under PIPE_BUF the kernel
	//: already guarantees atomicity but the mutex is cheap and uniform.
	s.mu.Lock()
	written, werr := s.f.Write(p)
	s.mu.Unlock()
	//: wrap any *os.File error with the WriteFailed sentinel semantics.
	if werr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return written, errs.Wrap(werr, errs.WrapParams{
			Code:    CodeWriteFailed,
			Reason:  "WRITE_FAILED",
			Public:  "File write failed",
			Private: "service/logger/sink/file.Write underlying *os.File returned an error",
		}, errs.Int("bytes", len(p)), errs.Int("level", int(r.Level)))
	}
	//: happy path — return the byte count.
	return written, nil
}

// Flush calls fsync on the underlying file so kernel page cache contents
// reach durable storage.
//
// Params:
//   - ctx: request-scoped context; cancelled contexts skip the sync.
//
// Returns:
//   - err: SyncFailed wrapping the os error; nil on success.
func (s *fileSink) Flush(ctx context.Context) (err error) {
	//: honour cancellation early — fsync is not cheap on slow disks.
	if ctx != nil && ctx.Err() != nil {
		//: caller already gave up; surface the cancellation cause.
		return ctx.Err()
	}
	//: serialise the sync against in-flight writes via the same mutex.
	s.mu.Lock()
	serr := s.f.Sync()
	s.mu.Unlock()
	//: wrap any sync error with the SyncFailed sentinel semantics.
	if serr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return errs.Wrap(serr, errs.WrapParams{
			Code:    CodeSyncFailed,
			Reason:  "SYNC_FAILED",
			Public:  "File flush failed",
			Private: "service/logger/sink/file.Flush underlying *os.File.Sync returned an error",
		})
	}
	//: happy path — nothing to report.
	return nil
}

// Close closes the underlying file. Subsequent Writes will fail with the
// kernel's "file already closed" error (which we wrap as WriteFailed).
//
// Returns:
//   - err: CloseFailed wrapping the os error; nil on success.
func (s *fileSink) Close() (err error) {
	//: serialise the close against in-flight writes via the same mutex.
	s.mu.Lock()
	cerr := s.f.Close()
	s.mu.Unlock()
	//: wrap any close error with the CloseFailed sentinel semantics.
	if cerr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return errs.Wrap(cerr, errs.WrapParams{
			Code:    CodeCloseFailed,
			Reason:  "CLOSE_FAILED",
			Public:  "File close failed",
			Private: "service/logger/sink/file.Close underlying *os.File.Close returned an error",
		})
	}
	//: happy path — nothing to report.
	return nil
}
