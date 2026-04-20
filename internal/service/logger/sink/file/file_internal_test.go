package file

import "testing"

func Test_defaultFilePerm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"defaultFilePerm is 0o644", 0o644},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if int(defaultFilePerm) != tc.want {
				t.Errorf("defaultFilePerm = %o, want %o", defaultFilePerm, tc.want)
			}
		})
	}
}

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
			if exitIOErr != tc.want {
				t.Errorf("exitIOErr = %d, want %d", exitIOErr, tc.want)
			}
		})
	}
}
