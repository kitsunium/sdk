package journald

import "testing"

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
			//: the I/O exit code must match the sysexits constant.
			if exitIOErr != tc.want {
				t.Errorf("%s: exitIOErr = %d, want %d", tc.name, exitIOErr, tc.want)
			}
		})
	}
}
