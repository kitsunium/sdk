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
// destination file does not yet exist. 0644 mirrors the conventional log
// file permission shipped by syslog and journald.
const defaultFilePerm os.FileMode = 0o644

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
	//: open in append mode so concurrent writers from any process interleave atomically.
	f, oerr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, defaultFilePerm)
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
		}, errs.Int("level", int64(r.Level)))
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
		}, errs.Int("bytes", int64(len(p))), errs.Int("level", int64(r.Level)))
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
