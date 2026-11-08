package rpc_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc/rpc"
)

// unreachedDialer fails the test if a refused configuration still dialled.
type unreachedDialer struct{ t *testing.T }

func (d unreachedDialer) Dial(context.Context) (*ipc.Conn, error) {
	d.t.Error("a configuration refused before the dial still dialled")
	return nil, context.Canceled
}

// TestTheFacadeRefusesABoundLeftAtZero reaches the engine through the
// forwarders, and its code through pkg/v1/errs: no bound has a default, and
// nothing is dialled for a configuration refused.
func TestTheFacadeRefusesABoundLeftAtZero(t *testing.T) {
	t.Parallel()
	_, err := rpc.NewServer(rpc.ServerConfig{}, rpc.Methods{})
	if !errs.HasCode(err, rpc.CodeMisconfigured) {
		t.Fatalf("NewServer with no bound: %v, want MISCONFIGURED", err)
	}
	_, err = rpc.NewClient(context.Background(), unreachedDialer{t}, rpc.ClientConfig{Format: "json"})
	if !errs.HasCode(err, rpc.CodeMisconfigured) {
		t.Fatalf("NewClient with no bound: %v, want MISCONFIGURED", err)
	}
}

// TestTheFacadesCodesAreTheCores pins the re-exported codes' values: a caller
// on either side of the socket branches on them.
func TestTheFacadesCodesAreTheCores(t *testing.T) {
	t.Parallel()
	for i, c := range []errs.Code{
		rpc.CodeMisconfigured, rpc.CodeFrameTooLarge, rpc.CodeCallTimeout, rpc.CodeCallCancelled,
		rpc.CodeMethodUnknown, rpc.CodeOverloaded, rpc.CodeProtocolViolation, rpc.CodeFormatRefused,
		rpc.CodeConnClosed, rpc.CodeRemoteFailed, rpc.CodeCodecFailed,
	} {
		if want := errs.Code(0x00_03_6A_00 + i + 1); c != want {
			t.Errorf("code %d is %s, want %s", i+1, c, want)
		}
	}
}
