// Package cgroup — white-box tests for the option accumulator.
package cgroup

import "testing"

// Test_defaultConfig pins the baseline. The default root is what a caller who
// passes no options gets, so it has to be the canonical unified mount rather
// than an empty string that would make every path relative to the cwd.
func Test_defaultConfig(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical unified mount", mountRoot}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := defaultConfig()
		if got.root != c.want {
			t.Errorf("defaultConfig().root = %q, want %q", got.root, c.want)
		}
		//: an empty root would make every joined path relative to the cwd.
		if got.root == "" {
			t.Error("defaultConfig() left the root empty")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_applyOptions pins the folding order and the nil tolerance. Options are
// commonly built conditionally — append one only when a flag is set — and the
// idiomatic way to express "no option" in such a slice is a nil entry, so a
// panic there would be a trap rather than a diagnostic.
func Test_applyOptions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		opts []Option
		want string
	}
	tests := []tc{
		{"no options at all", nil, mountRoot},
		{"an empty option slice", []Option{}, mountRoot},
		{"a single override", []Option{WithRoot("/a")}, "/a"},
		//: later options win, which is what lets a caller layer a default and
		//: then override it.
		{"the last override wins", []Option{WithRoot("/a"), WithRoot("/b")}, "/b"},
		{"a nil option is skipped", []Option{nil, WithRoot("/a"), nil}, "/a"},
		{"only nil options", []Option{nil, nil}, mountRoot},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := applyOptions(c.opts)
		if got.root != c.want {
			t.Errorf("applyOptions(%s).root = %q, want %q", c.name, got.root, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
