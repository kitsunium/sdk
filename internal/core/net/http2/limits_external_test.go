package http2_test

import (
	"errors"
	"strings"
	"testing"

	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestResolveDefaults holds the zero value at ADR 0195 §D3's numbers.
func TestResolveDefaults(t *testing.T) {
	v, err := corehttp2.LimitsValue{}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want := corehttp2.LimitsValue{
		MaxStreams: 100, ReceivePerStream: 256 << 10, ReceivePerConn: 1 << 20,
		MaxFrame: 16384, HeaderTable: 4096, MaxHeaderList: 8192,
	}
	if v != want {
		t.Fatalf("got %+v", v)
	}
}

// TestResolveKeeps holds an explicit value as given, and NoDynamicTable.
func TestResolveKeeps(t *testing.T) {
	in := corehttp2.LimitsValue{
		MaxStreams: 8, ReceivePerStream: 1 << 16, ReceivePerConn: 1 << 18,
		MaxFrame: 1 << 20, HeaderTable: 0, MaxHeaderList: 1024,
	}
	v, err := in.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	in.HeaderTable = 4096 // zero is the default, not "none"
	if v != in {
		t.Fatalf("got %+v, want %+v", v, in)
	}
	v, err = corehttp2.LimitsValue{HeaderTable: corehttp2.NoDynamicTable}.Resolve()
	if err != nil || v.HeaderTable != 0 {
		t.Fatalf("no dynamic table: %v %v", v.HeaderTable, err)
	}
	v, err = corehttp2.LimitsValue{ReceivePerStream: 1 << 17}.Resolve()
	if err != nil || v.ReceivePerConn != 4<<17 {
		t.Fatalf("per-conn default: %d %v", v.ReceivePerConn, err)
	}
}

// TestResolveRefuses names each refused field.
func TestResolveRefuses(t *testing.T) {
	cases := map[string]corehttp2.LimitsValue{
		"max_streams":        {MaxStreams: -1},
		"receive_per_stream": {ReceivePerStream: -4},
		"receive_per_conn":   {ReceivePerConn: 100, ReceivePerStream: 200},
		"max_frame":          {MaxFrame: 8 << 10},
		"header_table":       {HeaderTable: -2},
	}
	for field, v := range cases {
		_, err := v.Resolve()
		if !errors.Is(err, corehttp2.LimitsRefused) {
			t.Fatalf("%s: %v", field, err)
		}
		if !hasField(err, field) {
			t.Fatalf("%s: not named: %v", field, errs.FieldsOf(err))
		}
	}
	if _, err := (corehttp2.LimitsValue{MaxFrame: corehttp2.MaxFrameLimit + 1}).Resolve(); !errors.Is(err, corehttp2.LimitsRefused) {
		t.Fatalf("max frame past the protocol: %v", err)
	}
	if _, err := (corehttp2.LimitsValue{HeaderTable: 1 << 16}).Resolve(); !errors.Is(err, corehttp2.LimitsRefused) {
		t.Fatalf("header table past the range: %v", err)
	}
}

// hasField reports whether err's fields name the refused field.
func hasField(err error, field string) bool {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "reason" && strings.Contains(f.StringValue(), field) {
			return true
		}
	}
	return false
}

// TestErrCodeString names the RFC's codes, and a code outside it by number.
func TestErrCodeString(t *testing.T) {
	if got := corehttp2.ErrCodeFrameSize.String(); got != "FRAME_SIZE_ERROR" {
		t.Fatal(got)
	}
	if got := corehttp2.ErrCode(0x99).String(); got != "ERR_99" {
		t.Fatal(got)
	}
}
