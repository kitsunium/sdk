//go:build windows

// Package signal_test — the facade contract on Windows, where Relay has a
// backend of its own (TerminateProcess for a pid, a console control event for a
// process group): the reserved targets refused as on Unix, and a clean drain
// that delivers nothing returning nil. signal_other_test.go asserted the blanket
// refusal here until the first Windows run of the whole suite showed the backend
// it had missed.
package signal_test

import (
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/signal"
)

// TestRelayOnWindows pins both halves through the public names.
func TestRelayOnWindows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target signal.Target
		queued bool
		want   errs.Code
	}{
		//: 0 is the caller's own group and -1 every process it may signal: a
		//: mis-computed Target must never fan out that wide, on any platform.
		{name: "the reserved target 0 is refused before delivery", target: 0, queued: true, want: coreproc.CodeRelayFailed},
		{name: "the reserved target -1 is refused before delivery", target: -1, queued: true, want: coreproc.CodeRelayFailed},
		//: nothing queued, nothing delivered — a clean drain is nil.
		{name: "a clean drain delivers nothing and returns nil", target: 1 << 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := make(chan signal.Signal, 1)
			if tt.queued {
				src <- signal.Signal(syscall.SIGTERM)
			}
			close(src)
			err := signal.Relay(src, tt.target)
			if tt.want == 0 {
				if err != nil {
					t.Fatalf("Relay = %v, want nil", err)
				}
				return
			}
			if !errs.HasCode(err, tt.want) {
				t.Fatalf("Relay(target=%d) = %v, want code %v", tt.target, err, tt.want)
			}
		})
	}
}
