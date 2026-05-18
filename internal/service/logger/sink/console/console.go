// Package console implements a Sink that writes formatted records onto an
// io.Writer (typically os.Stdout or os.Stderr) under a local mutex so
// concurrent goroutines emit atomic lines. It is the default Sink shipped
// by pkg/v1/logger.Default.
//
// The console sink intentionally ignores the originating RecordEvent — it
// just streams the encoder's output verbatim. Sinks that need structured
// metadata (CloudWatch, S3, Kafka) read it from r in their own packages.
package console

import (
	"context"
	"io"
	"os"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// consoleSink is the default Sink wrapping an io.Writer behind a mutex.
type consoleSink struct {
	// w is the backing sink; writes are serialised through mu.
	w io.Writer
	// mu serialises Write calls so concurrent goroutines emit atomic lines.
	mu sync.Mutex
}

// New constructs a Sink that writes to w. Every Write call acquires the
// internal mutex so concurrent goroutines emit atomic lines.
func New(w io.Writer) (sink corelogger.Sink, err error) {
	//: reject nil writers early rather than panic at first Write.
	if w == nil {
		//: caller supplied no destination — return the documented sentinel.
		return nil, WriterNil
	}
	//: hand back the sink behind the public Sink interface.
	return &consoleSink{w: w}, nil
}

// NewStderr is a convenience constructor binding the sink to os.Stderr.
func NewStderr() corelogger.Sink {
	//: os.Stderr is non-nil by construction — bypass the validation entirely.
	return &consoleSink{w: os.Stderr}
}

// NewStdout is a convenience constructor binding the sink to os.Stdout.
func NewStdout() corelogger.Sink {
	//: os.Stdout is non-nil by construction — bypass the validation entirely.
	return &consoleSink{w: os.Stdout}
}

// Write streams p onto the underlying io.Writer under the local mutex.
func (s *consoleSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: honour context cancellation: cancelled contexts skip the write entirely.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so consumers get both our reason and stdlib Is().
		return 0, errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeCtxCancelled,
			Reason:  "CTX_CANCELLED",
			Public:  "Logging aborted due to cancellation",
			Private: "service/logger/sink/console.Write saw a cancelled context",
		}, errs.Int("level", int(r.Level)))
	}
	//: serialise writes so concurrent goroutines never interleave lines.
	s.mu.Lock()
	written, werr := s.w.Write(p)
	s.mu.Unlock()
	//: wrap any writer error with the WriteFailed sentinel semantics; the
	//: record's level is attached for diagnostics on multi-level pipelines.
	if werr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return written, errs.Wrap(werr, errs.WrapParams{
			Code:    CodeWriteFailed,
			Reason:  "WRITE_FAILED",
			Public:  "Console write failed",
			Private: "service/logger/sink/console.Write underlying writer returned an error",
		}, errs.Int("bytes", len(p)), errs.Int("level", int(r.Level)))
	}
	//: happy path — return the byte count.
	return written, nil
}

// Flush is a no-op for the console sink (writes are already synchronous).
func (s *consoleSink) Flush(ctx context.Context) error {
	//: honour cancellation even though there is nothing buffered to flush.
	if ctx != nil && ctx.Err() != nil {
		//: caller already gave up; surface the cancellation cause.
		return ctx.Err()
	}
	//: synchronous writer — nothing buffered to flush.
	return nil
}

// Close is a no-op for the console sink — the caller owns os.Stdout/Stderr
// and is responsible for closing custom io.Writers.
func (s *consoleSink) Close() error {
	//: caller owns the underlying writer; we never close stdout/stderr.
	return nil
}
