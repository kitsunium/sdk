// Package net — the duration token parser.
package net

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_DurationValue_parseString pins the unquoted-token half of the decoder.
// The empty string is the case worth isolating: it decodes to zero on purpose,
// because an operator who writes `timeout: ""` means "unset", while every other
// unparseable token is a mistake that must be refused rather than silently
// taken as zero.
func Test_DurationValue_parseString(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		token   string
		want    time.Duration
		wantErr bool
	}
	tests := []tc{
		{name: "an empty token means unset", token: "", want: 0},
		{name: "a whole second", token: "1s", want: time.Second},
		{name: "a compound token", token: "1m30s", want: 90 * time.Second},
		{name: "a sub-second token", token: "250ms", want: 250 * time.Millisecond},
		{name: "an explicit zero", token: "0s", want: 0},
		{name: "a negative token", token: "-5s", want: -5 * time.Second},
		{name: "a unitless number", token: "30", wantErr: true},
		{name: "prose", token: "thirty seconds", wantErr: true},
		{name: "a lone unit", token: "s", wantErr: true},
		{name: "whitespace", token: " ", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: seed with a non-zero value so a parser that failed to assign is not
		//: mistaken for one that decoded zero.
		d := DurationValue(time.Hour)

		err := d.parseString(c.token)

		if c.wantErr {
			if !errs.HasCode(err, CodeInvalidDuration) {
				t.Fatalf("parseString(%q) = %v, want INVALID_DURATION", c.token, err)
			}
			//: a refused token must leave the value alone rather than half-write
			//: it, so a partially decoded config never reaches a caller.
			if d != DurationValue(time.Hour) {
				t.Errorf("parseString(%q) overwrote the value with %v", c.token, d.Duration())
			}
			//: the offending token must ride along, or an operator gets
			//: "invalid duration" with no idea which field.
			var found bool
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "value" {
					found = true
				}
			}
			if !found {
				t.Errorf("parseString(%q) reported no value field: %v", c.token, errs.FieldsOf(err))
			}
			return
		}
		if err != nil {
			t.Fatalf("parseString(%q) = %v, want nil", c.token, err)
		}
		if d.Duration() != c.want {
			t.Errorf("parseString(%q) decoded %v, want %v", c.token, d.Duration(), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
