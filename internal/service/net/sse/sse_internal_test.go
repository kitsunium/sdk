// Package sse — the option resolution the public surface cannot observe.
package sse

import (
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_resolve pins ADR 0031 on both halves at once.
//
// A zero keep-alive is CLAMPED, because a stream with keep-alive silently
// disabled works perfectly on a developer's loopback and dies at one minute
// behind a real proxy — the worst possible place to learn it, and a working
// default that needs no explanation. A negative one is REFUSED, because no
// value the SDK invented for it would be defensible and "never" already has an
// explicit spelling. Neither half can be observed from outside the package
// without waiting fifteen seconds, which is why it is pinned here.
func Test_resolve(t *testing.T) {
	t.Parallel()
	type tc struct {
		name             string
		opts             []Option
		wantKeepAlive    time.Duration
		wantWriteTimeout time.Duration
		wantRetry        time.Duration
		wantDisabled     bool
		wantCode         errs.Code
	}
	tests := []tc{
		{
			name:             "an empty option set gets the working defaults",
			wantKeepAlive:    DefaultKeepAlive,
			wantWriteTimeout: DefaultWriteTimeout,
		},
		{
			name:             "a zero keep-alive is clamped, never taken as never",
			opts:             []Option{KeepAlive(0)},
			wantKeepAlive:    DefaultKeepAlive,
			wantWriteTimeout: DefaultWriteTimeout,
		},
		{
			name:             "a zero write budget is clamped the same way",
			opts:             []Option{WriteTimeout(0)},
			wantKeepAlive:    DefaultKeepAlive,
			wantWriteTimeout: DefaultWriteTimeout,
		},
		{
			name:             "an explicit interval is kept as given",
			opts:             []Option{KeepAlive(3 * time.Second), WriteTimeout(2 * time.Second)},
			wantKeepAlive:    3 * time.Second,
			wantWriteTimeout: 2 * time.Second,
		},
		{
			name:             "never is spelled out, not implied by a zero",
			opts:             []Option{WithoutKeepAlive()},
			wantKeepAlive:    DefaultKeepAlive,
			wantWriteTimeout: DefaultWriteTimeout,
			wantDisabled:     true,
		},
		{
			name:             "a later option deliberately wins",
			opts:             []Option{WithoutKeepAlive(), KeepAlive(time.Second)},
			wantKeepAlive:    time.Second,
			wantWriteTimeout: DefaultWriteTimeout,
		},
		{
			name:             "a retry hint survives resolution",
			opts:             []Option{Retry(4 * time.Second)},
			wantKeepAlive:    DefaultKeepAlive,
			wantWriteTimeout: DefaultWriteTimeout,
			wantRetry:        4 * time.Second,
		},
		{
			name:     "a negative keep-alive is refused, not clamped",
			opts:     []Option{KeepAlive(-time.Second)},
			wantCode: corenet.CodeSSEStreamMisconfigured,
		},
		{
			name:     "a negative write budget is refused, not clamped",
			opts:     []Option{WriteTimeout(-time.Second)},
			wantCode: corenet.CodeSSEStreamMisconfigured,
		},
		{
			name:     "an unrepresentable retry is refused by the frame that would carry it",
			opts:     []Option{Retry(500 * time.Microsecond)},
			wantCode: corenet.CodeSSEFieldInvalid,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := resolve(c.opts)
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("resolve() = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("resolve() = %v, want nil", err)
		}
		if got.keepAlive != c.wantKeepAlive {
			t.Errorf("keepAlive = %v, want %v", got.keepAlive, c.wantKeepAlive)
		}
		if got.writeTimeout != c.wantWriteTimeout {
			t.Errorf("writeTimeout = %v, want %v", got.writeTimeout, c.wantWriteTimeout)
		}
		if got.retry != c.wantRetry {
			t.Errorf("retry = %v, want %v", got.retry, c.wantRetry)
		}
		if got.noKeepAlive != c.wantDisabled {
			t.Errorf("noKeepAlive = %v, want %v", got.noKeepAlive, c.wantDisabled)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
