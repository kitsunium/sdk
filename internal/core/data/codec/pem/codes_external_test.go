// Package pem_test pins the codes internal/core/data/codec/pem declares: the
// value each was allocated with, which a move of its declaration never
// changes (ADR 0160 §3), and the sentinel bound to it.
package pem_test

import (
	"testing"

	corepem "github.com/kitsunium/sdk/internal/core/data/codec/pem"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestCodesKeepTheirValues pins every code to its literal value and to the
// sentinel that carries it, reason included. A failure here is a wire
// change: a dashboard, an alert rule or a client branches on these numbers.
func TestCodesKeepTheirValues(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		sentinel *errs.Error
		code     errs.Code
		want     uint32
		reason   string
	}
	tests := []tc{
		{"MarshalFailed", corepem.MarshalFailed, corepem.CodePEMMarshalFailed, 0x00_03_0A_01, "MARSHAL_FAILED"},
		{"UnmarshalFailed", corepem.UnmarshalFailed, corepem.CodePEMUnmarshalFailed, 0x00_03_0A_02, "UNMARSHAL_FAILED"},
		{"ValueInvalid", corepem.ValueInvalid, corepem.CodePEMValueInvalid, 0x00_03_0A_03, "VALUE_INVALID"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the value allocated with the range, unchanged.
		if uint32(c.code) != c.want {
			t.Errorf("%s: code = %#x, want %#x", c.name, uint32(c.code), c.want)
		}
		//: the sentinel carries that code.
		if got := c.sentinel.Code(); got != c.code {
			t.Errorf("%s: sentinel code = %v, want %v", c.name, got, c.code)
		}
		//: and the reason it has always rendered.
		if got := c.sentinel.Reason(); got != c.reason {
			t.Errorf("%s: sentinel reason = %q, want %q", c.name, got, c.reason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
