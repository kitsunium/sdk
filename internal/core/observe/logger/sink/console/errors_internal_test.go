package console

import "testing"

// Test_exitIOErr pins the EX_IOERR exit status WriteFailed carries — the
// design's `exit: IOERR` (ADR 0176) — so a design edit cannot silently switch
// the documented exit code.
func Test_exitIOErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"sysexits EX_IOERR is 74", 74},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := WriteFailed.ExitCode(); got != tc.want {
				t.Errorf("WriteFailed.ExitCode() = %d, want %d", got, tc.want)
			}
		})
	}
}
