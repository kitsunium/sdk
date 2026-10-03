//go:build !unix && !windows

// Package ipc — where there is no private socket: every configuration is
// refused.
package ipc

import (
	"os"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// prepareDir refuses: there is no Unix socket and no ACL to check here.
func prepareDir(dir string) error { return checkDir(dir) }

// checkDir refuses: this platform has no private socket (ADR 0018's uniform
// refusal, under this domain's code).
func checkDir(dir string) error {
	return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "no private socket on this platform"), errs.String("path", dir))
}

func ownerOf(os.FileInfo) (uid int, known bool) { return -1, false }
