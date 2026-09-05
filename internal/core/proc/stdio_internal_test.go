package proc

import "testing"

// The name table is what String reads, so a mode added to the constants
// without a name would silently render as "stdiomode(N)". Checking the table
// against the range catches that at the point it is introduced.
func Test_stdioModeNames(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mode StdioMode
		want string
	}
	tests := []tc{
		{"inherit", StdioInherit, "inherit"},
		{"null", StdioNull, "null"},
		{"capture", StdioCapture, "capture"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := stdioModeNames[c.mode]
		if !ok {
			t.Fatalf("mode %d has no name in the table", c.mode)
		}
		if got != c.want {
			t.Errorf("name = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}

	// Every mode Known accepts must have a name, or String would degrade to
	// the numeric form for a value the type calls valid.
	for m := StdioInherit; m.Known(); m++ {
		if _, ok := stdioModeNames[m]; !ok {
			t.Errorf("mode %d is Known but unnamed", m)
		}
	}
	if len(stdioModeNames) != int(StdioCapture)+1 {
		t.Errorf("table holds %d names for %d modes", len(stdioModeNames), int(StdioCapture)+1)
	}
}
