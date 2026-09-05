// Package reaper — the cross-platform option accumulator.
package reaper

import "testing"

// Test_resolve pins the folding and the nil tolerance. Options are commonly
// built conditionally — append one only when a flag is set — and the idiomatic
// way to express "no option" in such a slice is a nil entry, so a panic there
// would be a trap rather than a diagnostic.
func Test_resolve(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many real observers to install, and where the nils go.
		opts     func() []Option
		wantHook bool
	}
	tests := []tc{
		{name: "no options at all", opts: func() []Option { return nil }},
		{name: "an empty slice", opts: func() []Option { return []Option{} }},
		{name: "only nil entries", opts: func() []Option { return []Option{nil, nil} }},
		{
			name:     "one observer",
			opts:     func() []Option { return []Option{WithOnReap(func(int) {})} },
			wantHook: true,
		},
		{
			name:     "an observer between nils",
			opts:     func() []Option { return []Option{nil, WithOnReap(func(int) {}), nil} },
			wantHook: true,
		},
		{
			//: later options win, which is what lets a caller layer a default
			//: and then override it.
			name: "the last observer wins",
			opts: func() []Option {
				return []Option{WithOnReap(func(int) {}), WithOnReap(nil)}
			},
			wantHook: false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := resolve(c.opts())
		if (got.onReap != nil) != c.wantHook {
			t.Errorf("an observer is installed = %v, want %v", got.onReap != nil, c.wantHook)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
