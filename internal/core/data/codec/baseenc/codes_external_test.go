// Package baseenc_test pins the codes internal/core/data/codec/baseenc declares: the
// value each was allocated with, which a move of its declaration never
// changes (ADR 0160 §3), and the sentinel bound to it.
package baseenc_test

import (
	"testing"

	corebaseenc "github.com/kitsunium/sdk/internal/core/data/codec/baseenc"
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
		{"BaseEncMarshalFailed", corebaseenc.BaseEncMarshalFailed, corebaseenc.CodeBaseEncMarshalFailed, 0x00_03_18_01, "BASE_ENC_MARSHAL_FAILED"},
		{"BaseEncUnmarshalFailed", corebaseenc.BaseEncUnmarshalFailed, corebaseenc.CodeBaseEncUnmarshalFailed, 0x00_03_18_02, "BASE_ENC_UNMARSHAL_FAILED"},
		{"BaseEncDecodeFailed", corebaseenc.BaseEncDecodeFailed, corebaseenc.CodeBaseEncDecodeFailed, 0x00_03_18_03, "BASE_ENC_DECODE_FAILED"},
		{"BaseEncSizeExceeded", corebaseenc.BaseEncSizeExceeded, corebaseenc.CodeBaseEncSizeExceeded, 0x00_03_18_04, "BASE_ENC_SIZE_EXCEEDED"},
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
