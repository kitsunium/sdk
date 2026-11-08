//go:build !(linux && (amd64 || arm64))

package service

import (
	"context"
	"os"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// openImage is refused here: no O_PATH, no memfd whose number this engine
// knows.
func openImage(path string) (*os.File, error) {
	//: UNSUPPORTED_PLATFORM naming the call.
	return nil, errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{}, errs.String("op", "OpenImage"), errs.String("path", path))
}

// reexec is refused here, as openImage is.
func reexec(_ *HandoverState, _ *os.File, _, _ []string) error {
	//: UNSUPPORTED_PLATFORM naming the call.
	return errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{}, errs.String("op", "Reexec"))
}

// takeHandover is nil here: no image hands over.
func takeHandover(_ context.Context, _ time.Duration) (*HandoverState, error) {
	//: nothing.
	return nil, nil
}
