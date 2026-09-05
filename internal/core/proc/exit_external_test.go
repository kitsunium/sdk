package proc_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Success is the one predicate a supervisor branches on, and it must answer
// for both halves of the outcome: a signalled process did not succeed even
// when Code happens to read zero, which it does on the platforms that leave
// Code untouched after a signal.
func Test_ExitValue_Success(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		exit coreproc.ExitValue
		want bool
	}
	tests := []tc{
		{"a clean exit", coreproc.ExitValue{Code: 0}, true},
		{"a non-zero status", coreproc.ExitValue{Code: 1}, false},
		{"the conventional 'command not found'", coreproc.ExitValue{Code: 127}, false},
		{
			// The reason Signaled is a separate field: a signalled process
			// leaves Code at whatever the platform put there, so status alone
			// cannot answer the question.
			"signalled with a zero status is still a failure",
			coreproc.ExitValue{Code: 0, Signaled: true},
			false,
		},
		{
			"signalled with the conventional -1 status",
			coreproc.ExitValue{Code: -1, Signaled: true},
			false,
		},
		{"the zero value is a success", coreproc.ExitValue{}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.exit.Success(); got != c.want {
			t.Errorf("Success() = %v, want %v (%+v)", got, c.want, c.exit)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The accounting fields are carried verbatim: nothing in the value type
// interprets them, so a supervisor reading MaxRSS gets what wait4 reported.
func Test_ExitValue_accounting(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		exit coreproc.ExitValue
		want int64
	}
	tests := []tc{
		{"a platform that reports rusage", coreproc.ExitValue{MaxRSS: 4096}, 4096},
		{"a platform that does not", coreproc.ExitValue{}, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.exit.MaxRSS; got != c.want {
			t.Errorf("MaxRSS = %d, want %d", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
