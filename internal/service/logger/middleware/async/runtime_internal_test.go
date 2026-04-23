package async

import (
	"testing"
)

func Test_yieldOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"yieldOnce returns without panicking"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				yieldOnce()
			}()
			if panicked {
				t.Error("yieldOnce panicked; runtime.Gosched is panic-free by contract")
			}
		})
	}
}

func Test_isClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		closeChan bool
		want      bool
	}{
		{"open channel reports false", false, false},
		{"closed channel reports true", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ch := make(chan struct{})
			if tc.closeChan {
				close(ch)
			}
			if got := isClosed(ch); got != tc.want {
				t.Errorf("isClosed = %v, want %v", got, tc.want)
			}
		})
	}
}

func Test_asyncCtx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"asyncCtx returns a non-nil context with no deadline"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := asyncCtx()
			//: contract: never nil, no deadline, no cancellation.
			if ctx == nil {
				t.Fatal("asyncCtx returned nil")
				return
			}
			if _, hasDeadline := ctx.Deadline(); hasDeadline {
				t.Error("asyncCtx returned a context with a deadline; want background")
			}
			if ctx.Err() != nil {
				t.Errorf("asyncCtx returned a context with err = %v, want nil", ctx.Err())
			}
		})
	}
}

func Test_forwardDownstreamError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		bytes       int
		err         error
		wantInvoked bool
	}{
		{"happy path is a no-op", 5, nil, false},
		{"non-nil error is surfaced to onError", 5, errBoom{}, true},
		{"negative bytes is defensively ignored", -1, errBoom{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var invoked bool
			//: drive a minimal sink with a recording callback so we can
			//: assert the no-op and fire branches deterministically.
			s := &asyncSink{onError: func(error) { invoked = true }}
			s.forwardDownstreamError(tc.bytes, tc.err)
			if invoked != tc.wantInvoked {
				t.Errorf("onError invoked=%v, want %v", invoked, tc.wantInvoked)
			}
		})
	}
}

func Test_swallowRingError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"nil error is a no-op", nil},
		{"non-nil error is silently dropped", errBoom{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			swallowRingError(tc.err)
		})
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
