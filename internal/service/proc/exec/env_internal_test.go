package exec

import (
	"slices"
	"testing"
)

// Test_envKey pins what a name is: everything before the first "=" after its
// first byte. A leading "=" belongs to the name, as os/exec reads it: Windows
// keeps each drive's current directory as "=C:=C:\dir", and "=C:" and "=D:" are
// two variables.
func Test_envKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name, entry, want string
	}
	tests := []tc{
		{"an assignment", "A=1", "A"},
		{"an empty value", "A=", "A"},
		{"a value holding =", "A=b=c", "A"},
		{"no assignment is all key", "FLAG", "FLAG"},
		{"nothing", "", ""},
		{"a drive's directory", `=C:=C:\x`, "=C:"},
		{"another drive's", `=D:=D:\y`, "=D:"},
		{"a lone =", "=", "="},
		{"= and no assignment after it", "=foo", "=foo"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := envKey(c.entry); got != c.want {
			t.Errorf("envKey(%q) = %q, want %q", c.entry, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_EnvDeny_driveEntries: "=C:" and "=D:" are two variables to the filters
// too; they used to share the empty key and overwrite each other.
func Test_EnvDeny_driveEntries(t *testing.T) {
	t.Parallel()
	parent := []string{`=C:=C:\x`, `=D:=D:\y`, "A=1"}
	if got := EnvDeny(parent, "B"); !slices.Equal(got, parent) {
		t.Errorf("EnvDeny(%q) = %q, want every entry", parent, got)
	}
}
