// Package logger — a Logger that owns the writers it was built over, and the
// one call that releases them.
package logger

import (
	"io"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// onceCloser releases what it wraps exactly once, and answers every later
// Close with the first answer: a second Close of a file-backed writer is the
// kernel's "file already closed", which is not news to anyone.
type onceCloser struct {
	// once guards the single release.
	once sync.Once
	// target is what is released.
	target io.Closer
	// err is the first Close's answer, repeated to every later caller.
	err error
}

// Close releases the target on the first call and repeats its answer after.
func (o *onceCloser) Close() error {
	//: the one release.
	o.once.Do(func() {
		o.err = o.target.Close()
	})
	//: the first answer, every time.
	return o.err
}

// Owning returns lg carrying owned as the resources it owns, so that Close
// releases them.
//
// It exists for a constructor that OPENS writers on its caller's behalf —
// pkg/v1/logger.NewMulti resolves each spec through the writer registry, so the
// caller never holds a Sink it could close — and hands back a Logger that was
// otherwise the only thing referring to them. Without it those files were held
// until a collection ran their finalizers: a descriptor leak for a program that
// rebuilds its logger, and on Windows a log file nothing could delete or rotate
// while the process lived (ADR 0095).
//
// The result is still this package's concrete Logger, so Build and LogAttrs
// keep their fast path. A Logger this package did not build is returned
// unchanged — it has no Close to carry the resources to.
func Owning(lg corelogger.Logger, owned io.Closer) corelogger.Logger {
	impl, ok := lg.(*loggerImpl)
	//: a foreign Logger, or nothing to own.
	if !ok || owned == nil {
		//: unchanged.
		return lg
	}
	//: a copy, so the Logger passed in stays the non-owning one it was.
	owning := *impl
	owning.owned = &onceCloser{target: owned}
	//: the same handler and trace binding, now owning the writers.
	return &owning
}

// Close releases the writers this Logger owns — once, whatever the number of
// calls — and reports the first release's outcome.
//
// A Logger that owns nothing returns nil: every Logger derived with With or
// WithGroup — WithGroup("") included — shares its parent's writers and owns
// none of them, so closing a child can never pull the files out from under its
// parent or its siblings.
// Records logged after the owner's Close are dropped, as a failing write
// always is (the Logger contract never surfaces one).
func (l *loggerImpl) Close() error {
	//: nothing opened on this Logger's behalf.
	if l.owned == nil {
		//: nothing to release.
		return nil
	}
	//: the writers, once.
	return l.owned.Close()
}
