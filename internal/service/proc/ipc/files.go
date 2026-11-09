//go:build unix || windows

package ipc

import (
	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fdPassRefused wraps FDPassRefused with its reason and, when there is one,
// the cause.
func fdPassRefused(reason string, cause error) error {
	if cause == nil {
		return errs.Wrap(coreipc.FDPassRefused, errs.WrapParams{}, errs.String("reason", reason))
	}
	return errs.Wrap(coreipc.FDPassRefused, errs.WrapParams{}, errs.String("reason", reason), errs.String("cause", cause.Error()))
}
