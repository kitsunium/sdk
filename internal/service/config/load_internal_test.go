// Package config — white-box tests for the decode step.
package config

import (
	"math"
	"testing"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_decodeInto pins the JSON round trip the loader uses to map a merged
// layer onto a typed target.
//
// Going through encoding/json is what buys struct-tag mapping for free, and it
// is also where a type mismatch surfaces — so the failure has to arrive as
// CONFIG_DECODE_FAILED and not as a raw json error a caller cannot classify.
func Test_decodeInto(t *testing.T) {
	t.Parallel()
	type target struct {
		Port int      `json:"port"`
		Host string   `json:"host"`
		Tags []string `json:"tags"`
	}

	type tc struct {
		name    string
		merged  map[string]any
		want    target
		wantErr bool
	}
	tests := []tc{
		{name: "an empty layer leaves the zero value", merged: map[string]any{}},
		{
			name:   "every field set",
			merged: map[string]any{"port": 8080, "host": "kitsune", "tags": []any{"a", "b"}},
			want:   target{Port: 8080, Host: "kitsune", Tags: []string{"a", "b"}},
		},
		{
			//: an int64 is what the env Source produces for an integral token,
			//: so it must land in an int field without complaint.
			name:   "an int64 into an int field",
			merged: map[string]any{"port": int64(8080)},
			want:   target{Port: 8080},
		},
		{
			name:   "a key the target does not declare is ignored",
			merged: map[string]any{"port": 1, "unknown": "x"},
			want:   target{Port: 1},
		},
		{
			name:    "a string where a number belongs",
			merged:  map[string]any{"port": "not a number"},
			wantErr: true,
		},
		{
			name:    "an object where a scalar belongs",
			merged:  map[string]any{"host": map[string]any{"a": 1}},
			wantErr: true,
		},
		{
			//: a value encoding/json cannot marshal fails on the way OUT,
			//: which is the other half of the round trip.
			name:    "a value that cannot be marshalled",
			merged:  map[string]any{"port": math.Inf(1)},
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got target

		err := decodeInto(c.merged, &got)

		if c.wantErr {
			if !errs.HasCode(err, coreconfig.CodeConfigDecodeFailed) {
				t.Fatalf("decodeInto(%s) = %v, want CONFIG_DECODE_FAILED", c.name, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("decodeInto(%s) = %v, want nil", c.name, err)
		}
		if got.Port != c.want.Port || got.Host != c.want.Host {
			t.Errorf("decodeInto(%s) = %+v, want %+v", c.name, got, c.want)
		}
		if len(got.Tags) != len(c.want.Tags) {
			t.Errorf("decodeInto(%s) tags = %v, want %v", c.name, got.Tags, c.want.Tags)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
