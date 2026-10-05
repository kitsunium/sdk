package logger

import (
	"io"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
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

// owning is Owning's body: decl_gen.go writes Owning, from the
// design, as one call of it.
func owning(lg corelogger.Logger, owned io.Closer) corelogger.Logger {
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
