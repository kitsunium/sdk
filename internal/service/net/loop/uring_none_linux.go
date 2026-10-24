//go:build linux && !(amd64 || arm64 || riscv64 || loong64)

package loop

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// startIOURing refuses: this architecture is outside io_uring's wave 1
// (ADR 0186 §D5), and epoll serves it.
func startIOURing(_ *Group, _ *Config, _ loopHooks) error {
	//: refused by name.
	return errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
		errs.String("stage", "arch"), errs.String("reason", "io_uring is wave 1 on amd64, arm64, riscv64 and loong64 only"))
}
