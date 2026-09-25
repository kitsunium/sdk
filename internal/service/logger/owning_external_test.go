package logger_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
)

// countingCloser records how often it was released and answers with err.
type countingCloser struct {
	calls int
	err   error
}

// Close implements io.Closer.
func (c *countingCloser) Close() error {
	c.calls++
	return c.err
}

// foreignLogger is a Logger this package did not build.
type foreignLogger struct{ corelogger.Logger }

// TestAnOwningLoggerReleasesWhatItOwnsOnce pins Owning and Close: the owner
// releases its writers exactly once whatever the number of calls, a Logger
// derived from it owns nothing and releases nothing, the owner keeps the
// package's fast path, and a Logger this package did not build is left alone.
//
// It is what lets pkg/v1/logger.NewMulti hand back a Logger over writers the
// caller never held: before it, nothing could close them, and on Windows their
// files could be neither deleted nor rotated while the process lived.
func TestAnOwningLoggerReleasesWhatItOwnsOnce(t *testing.T) {
	t.Parallel()
	base, err := svclogger.New(mustNewText(t, &bytes.Buffer{}, level.Info))
	//: a plain Logger to hand the writers to.
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	refused := errors.New("the writer refused to close")
	writers := &countingCloser{err: refused}
	owner := svclogger.Owning(base, writers)

	closer, ok := owner.(io.Closer)
	//: the owner exposes the release.
	if !ok {
		t.Fatalf("an owning Logger (%T) does not implement io.Closer", owner)
	}
	//: every child shares the writers; closing one must release nothing —
	//: WithGroup("") included, whose no-op used to hand the owner itself back.
	children := map[string]corelogger.Logger{
		"With":          owner.With(corelogger.AttrValue{Key: "k", Value: corelogger.StringValue("v")}),
		"WithGroup":     owner.WithGroup("g"),
		`WithGroup("")`: owner.WithGroup(""),
	}
	//: each way of deriving a child.
	for how, derived := range children {
		child, isCloser := derived.(io.Closer)
		//: a child is still this package's Logger, so it has a Close.
		if !isCloser {
			t.Fatalf("a Logger derived with %s does not implement io.Closer", how)
		}
		//: and that Close owns nothing.
		if cerr := child.Close(); cerr != nil || writers.calls != 0 {
			t.Fatalf("closing a Logger derived with %s = %v and released the writers %d time(s), want nil and none", how, cerr, writers.calls)
		}
	}
	//: the owner releases them once, and repeats the first answer.
	for attempt := range 3 {
		//: every call repeats the first Close's answer.
		if cerr := closer.Close(); !errors.Is(cerr, refused) {
			t.Fatalf("Close attempt %d = %v, want the writers' own answer", attempt+1, cerr)
		}
	}
	//: and the writers saw exactly one release.
	if writers.calls != 1 {
		t.Fatalf("the writers were released %d times, want exactly once", writers.calls)
	}
	//: the Logger passed in stays the non-owning one it was.
	if cerr := base.(io.Closer).Close(); cerr != nil || writers.calls != 1 {
		t.Fatalf("closing the Logger Owning was given = %v, want nil and no release", cerr)
	}
	//: still this package's concrete Logger, so the fluent fast path answers.
	if svclogger.Build(owner, level.Info) == nil {
		t.Fatal("Build on an owning Logger returned nil — the fast path's type assertion was lost")
	}
	//: a foreign Logger has no Close to carry anything to.
	foreign := foreignLogger{Logger: base}
	//: returned as it came, carrying nothing.
	if got := svclogger.Owning(foreign, writers); got != corelogger.Logger(foreign) {
		t.Fatalf("Owning(foreign) = %T, want the foreign Logger unchanged", got)
	}
}
