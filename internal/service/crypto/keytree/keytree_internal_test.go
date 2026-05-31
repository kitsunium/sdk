package keytree

import "testing"

// Test_encodePath asserts the length-prefixed encoding is injective.
func Test_encodePath(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated; want is the single discriminator
	cases := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{name: "collision-guard", a: []string{"a/b", "c"}, b: []string{"a", "b/c"}, want: false},
		{name: "same-path", a: []string{"x", "y"}, b: []string{"x", "y"}, want: true},
		{name: "empty-vs-empty-seg", a: []string{""}, b: nil, want: false},
	}
	check := func(t *testing.T, a, b []string, want bool) {
		t.Helper()
		//: equal encodings must occur only for equal canonical paths
		if got := encodePath(a) == encodePath(b); got != want {
			t.Fatalf("encodePath equality = %v want %v", got, want)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.a, tc.b, tc.want)
		})
	}
}
