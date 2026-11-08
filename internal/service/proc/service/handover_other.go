//go:build !linux

package service

import (
	"context"
	"os"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// handoverHere refuses op: the hand-over is Linux's (its store, its
// children reclaimed through /proc).
func handoverHere(op string) error {
	//: UNSUPPORTED_PLATFORM naming the call.
	return errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{}, errs.String("op", op))
}

// freeze is never asked here: Freeze refuses first.
func (a *actor) freeze(req *event) {
	a.u.answer([]uint64{req.waiter}, handoverHere("Freeze"))
}

// restore is never asked here: Resume refuses first.
func (a *actor) restore(_ *restoring) {}

// adoptNotify is refused here.
func adoptNotify(_ *os.File, _ string) (coresvc.NotifySource, error) {
	//: UNSUPPORTED_PLATFORM.
	return nil, handoverHere("Resume")
}

// fileIdentity is refused here.
func fileIdentity(_ *os.File) (fileRecord, error) {
	//: UNSUPPORTED_PLATFORM.
	return fileRecord{}, handoverHere("Reexec")
}

// stopHanded holds nothing here: no hand-over was made.
func stopHanded(_ context.Context, _ time.Duration, _ []handedProc) {}
