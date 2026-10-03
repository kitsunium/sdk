// Package tlv_test pins the codes internal/core/data/codec/tlv declares: the
// value each was allocated with, which a move of its declaration never
// changes (ADR 0160 §3), and the sentinel bound to it.
package tlv_test

import (
	"testing"

	coretlv "github.com/kitsunium/sdk/internal/core/data/codec/tlv"
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
		{"MarshalFailed", coretlv.MarshalFailed, coretlv.CodeTLVMarshalFailed, 0x00_03_16_01, "MARSHAL_FAILED"},
		{"UnmarshalFailed", coretlv.UnmarshalFailed, coretlv.CodeTLVUnmarshalFailed, 0x00_03_16_02, "UNMARSHAL_FAILED"},
		{"UnsupportedType", coretlv.UnsupportedType, coretlv.CodeTLVUnsupportedType, 0x00_03_16_03, "UNSUPPORTED_TYPE"},
		{"DepthExceeded", coretlv.DepthExceeded, coretlv.CodeTLVDepthExceeded, 0x00_03_16_04, "DEPTH_EXCEEDED"},
		{"SizeExceeded", coretlv.SizeExceeded, coretlv.CodeTLVSizeExceeded, 0x00_03_16_05, "SIZE_EXCEEDED"},
		{"Truncated", coretlv.Truncated, coretlv.CodeTLVTruncated, 0x00_03_16_06, "TRUNCATED"},
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
