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

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	corefile "github.com/kitsunium/sdk/internal/core/observe/logger/sink/file"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/logger/internal/logfile"
)

// defaultFilePerm is the permission bitmask passed to os.OpenFile when the
// destination file does not yet exist. 0600 restricts reads to the owning
// UID so diagnostic content (attr values, wrapped Private fields bubbled
// in by consumers that log the Source chain) is never world-readable.
// Operators that need group or other-reader access chmod explicitly.
const defaultFilePerm os.FileMode = 0o600

// openRefusals is this sink's vocabulary for the hardened open both file sinks
// share (internal/service/observe/logger/internal/logfile): one sentinel, CodeOpenFailed, for
// the policy refusal of a planted link and for every failed open — the kernel's
// refusal of a link planted after the check among them — told apart by the
// "kind" field the shared open adds.
var openRefusals = logfile.RefusalSpec{
	Symlink: errs.WrapParams{
		Code:    corefile.CodeOpenFailed,
		Reason:  "OPEN_FAILED",
		Public:  "File sink refuses to open a symlink",
		Private: "service/observe/logger/sink/file.New: path is a symlink; refusing per hardening policy",
	},
	Open: errs.WrapParams{
		Code:    corefile.CodeOpenFailed,
		Reason:  "OPEN_FAILED",
		Public:  "File sink could not open the destination file",
		Private: "service/observe/logger/sink/file.New: os.OpenFile returned an error",
	},
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
func New(path string) (sink corelogger.Sink, err error) {
	//: refuse an empty path so callers detect the misconfiguration immediately.
	if path == "" {
		//: documented sentinel — caller must supply a path.
		return nil, corefile.PathEmpty
	}
	//: reject a pre-existing symlink (CWE-59), then open with O_NOFOLLOW where
	//: the platform has it, so a link planted between the check and the open
	//: fails the open rather than silently redirecting the sink — the hardened
	//: open both file sinks share, refusing under this sink's OPEN_FAILED.
	f, oerr := logfile.Open(path, defaultFilePerm, &openRefusals)
	//: the refusal already carries the code, the path and, for a link, the kind.
	if oerr != nil {
		//: surface the typed refusal for HasCode-style introspection.
		return nil, oerr
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
				Code:    corefile.CodeCloseFailed,
				Reason:  "CLOSE_FAILED",
				Public:  "File close failed",
				Private: "service/observe/logger/sink/file.New: deferred close failed after another error",
			})
		}
	}()
	//: ownership transferred to the sink; sink.Close releases the descriptor.
	return &fileSink{f: f}, nil
}

// Write appends p onto the underlying file under the local mutex.
func (s *fileSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: honour context cancellation: cancelled contexts skip the write entirely.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so consumers get both our reason and stdlib Is().
		return 0, errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    corefile.CodeCtxCancelled,
			Reason:  "CTX_CANCELLED",
			Public:  "Logging aborted due to cancellation",
			Private: "service/observe/logger/sink/file.Write saw a cancelled context",
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
			Code:    corefile.CodeWriteFailed,
			Reason:  "WRITE_FAILED",
			Public:  "File write failed",
			Private: "service/observe/logger/sink/file.Write underlying *os.File returned an error",
		}, errs.Int("bytes", len(p)), errs.Int("level", int(r.Level)))
	}
	//: happy path — return the byte count.
	return written, nil
}

// Flush calls fsync on the underlying file so kernel page cache contents
// reach durable storage.
func (s *fileSink) Flush(ctx context.Context) error {
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
			Code:    corefile.CodeSyncFailed,
			Reason:  "SYNC_FAILED",
			Public:  "File flush failed",
			Private: "service/observe/logger/sink/file.Flush underlying *os.File.Sync returned an error",
		})
	}
	//: happy path — nothing to report.
	return nil
}

// Close closes the underlying file. Subsequent Writes will fail with the
// kernel's "file already closed" error (which we wrap as WriteFailed).
func (s *fileSink) Close() error {
	//: serialise the close against in-flight writes via the same mutex.
	s.mu.Lock()
	cerr := s.f.Close()
	s.mu.Unlock()
	//: wrap any close error with the CloseFailed sentinel semantics.
	if cerr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return errs.Wrap(cerr, errs.WrapParams{
			Code:    corefile.CodeCloseFailed,
			Reason:  "CLOSE_FAILED",
			Public:  "File close failed",
			Private: "service/observe/logger/sink/file.Close underlying *os.File.Close returned an error",
		})
	}
	//: happy path — nothing to report.
	return nil
}
