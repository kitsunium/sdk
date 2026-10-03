// Package strictjson_test pins the codes internal/core/data/codec/strictjson declares: the
// value each was allocated with, which a move of its declaration never
// changes (ADR 0160 §3), and the sentinel bound to it.
package strictjson_test

import (
	"testing"

	corestrictjson "github.com/kitsunium/sdk/internal/core/data/codec/strictjson"
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
		{"DocumentTooLarge", corestrictjson.DocumentTooLarge, corestrictjson.CodeDocumentTooLarge, 0x00_03_48_01, "DOCUMENT_TOO_LARGE"},
		{"DocumentEmpty", corestrictjson.DocumentEmpty, corestrictjson.CodeDocumentEmpty, 0x00_03_48_02, "DOCUMENT_EMPTY"},
		{"DocumentMalformed", corestrictjson.DocumentMalformed, corestrictjson.CodeDocumentMalformed, 0x00_03_48_03, "DOCUMENT_MALFORMED"},
		{"MemberUnknown", corestrictjson.MemberUnknown, corestrictjson.CodeMemberUnknown, 0x00_03_48_04, "MEMBER_UNKNOWN"},
		{"ValueMismatched", corestrictjson.ValueMismatched, corestrictjson.CodeValueMismatched, 0x00_03_48_05, "VALUE_MISMATCHED"},
		{"MediaTypeUnsupported", corestrictjson.MediaTypeUnsupported, corestrictjson.CodeMediaTypeUnsupported, 0x00_03_48_06, "MEDIA_TYPE_UNSUPPORTED"},
		{"DocumentUnreadable", corestrictjson.DocumentUnreadable, corestrictjson.CodeDocumentUnreadable, 0x00_03_48_07, "DOCUMENT_UNREADABLE"},
		{"DecodeMisconfigured", corestrictjson.DecodeMisconfigured, corestrictjson.CodeDecodeMisconfigured, 0x00_03_48_08, "DECODE_MISCONFIGURED"},
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

// TestRefusalsKeepTheirStatuses pins the HTTP status and the exit code every
// refusal carries: they were net/http's and sysexits' constants before the
// declarations moved to the core, which spells them as numbers.
func TestRefusalsKeepTheirStatuses(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		sentinel *errs.Error
		status   int
		exit     int
	}
	tests := []tc{
		{"DocumentTooLarge", corestrictjson.DocumentTooLarge, 413, 65},
		{"DocumentEmpty", corestrictjson.DocumentEmpty, 400, 65},
		{"DocumentMalformed", corestrictjson.DocumentMalformed, 400, 65},
		{"MemberUnknown", corestrictjson.MemberUnknown, 400, 65},
		{"ValueMismatched", corestrictjson.ValueMismatched, 400, 65},
		{"MediaTypeUnsupported", corestrictjson.MediaTypeUnsupported, 415, 65},
		{"DocumentUnreadable", corestrictjson.DocumentUnreadable, 400, 74},
		{"DecodeMisconfigured", corestrictjson.DecodeMisconfigured, 500, 70},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the status a server answers with.
		if got := c.sentinel.HTTPStatus(); got != c.status {
			t.Errorf("%s: HTTP status = %d, want %d", c.name, got, c.status)
		}
		//: the status a command exits with.
		if got := c.sentinel.ExitCode(); got != c.exit {
			t.Errorf("%s: exit code = %d, want %d", c.name, got, c.exit)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
