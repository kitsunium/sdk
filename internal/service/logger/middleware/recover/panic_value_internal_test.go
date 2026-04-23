package recover

import (
	"strings"
	"testing"
)

func Test_panicValue_Error(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  any
		want string
	}{
		{"string panic value", "boom", "panic: boom"},
		{"int panic value", 42, "panic: 42"},
		{"nil panic value", nil, "panic: <nil>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := panicValue{v: tc.val}
			if got := p.Error(); !strings.Contains(got, tc.want) {
				t.Errorf("Error() = %q, want to contain %q", got, tc.want)
			}
		})
	}
}
