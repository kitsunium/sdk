//go:build !linux

package loop

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// startNative refuses: no native backend exists off Linux in this wave
// (ADR 0186 §D5); server serves the group on the portable backend.
func startNative(_ *Group, cfg *Config) error {
	//: the shared sentinel, wrapped, never redeclared (ADR 0018 §(a)).
	return errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
		errs.String("group", cfg.Group), errs.String("backend", cfg.Value.Backend.String()))
}

// descriptorLimit has no native loop to size off Linux; one keeps resolve
// past its zero check so the refusal is UnsupportedPlatform's.
func descriptorLimit() uint64 {
	//: any positive value.
	return 1
}

// closeListeners has nothing to close: no platform here hands one over.
func closeListeners(_ []Listener) {
	//: nothing was handed over.
}
