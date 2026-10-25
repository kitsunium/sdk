// Package net_test — the engine families a group may name.
package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_EngineValue_String pins the spelling of every engine family, which
// EngineUnsupported's "engine" field carries, and that the zero value is auto.
func Test_EngineValue_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		engine corenet.EngineValue
		want   string
	}
	tests := []tc{
		{"the zero value", corenet.EngineValue(0), "auto"},
		{"goroutine", corenet.EngineGoroutine, "goroutine"},
		{"loop", corenet.EngineLoop, "loop"},
		//: past the block, the value is reported as such.
		{"one past the last engine", corenet.EngineLoop + 1, "unknown"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.engine.String(); got != c.want {
			t.Errorf("EngineValue(%d).String() = %q, want %q", uint8(c.engine), got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_OptionKind_values pins that the kinds added after PreHandshake shifted
// none before them: a kind's value is what a LoopOptionHonourer compiled
// against an earlier release answers for.
func Test_OptionKind_values(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		kind corenet.OptionKind
		want uint8
	}
	tests := []tc{
		{"pre-handshake keeps its value", corenet.OptionPreHandshake, 15},
		{"plaintext follows it", corenet.OptionPlaintext, 16},
		{"name", corenet.OptionName, 17},
		{"engine", corenet.OptionEngine, 18},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if uint8(c.kind) != c.want {
			t.Errorf("%s = %d, want %d", c.kind, uint8(c.kind), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
