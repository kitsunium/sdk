package docstore

import (
	"regexp"
	"testing"
)

// TestATableNameIsALowerCaseIdentifier pins isTableName to the shape it
// replaced, `^[a-z_][a-z0-9_]*$`, on every near miss: written out rather than
// a regular expression compiled at the package's initialisation, which every
// program importing the store paid at start (#276).
func TestATableNameIsALowerCaseIdentifier(t *testing.T) {
	t.Parallel()
	shape := regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	for _, name := range []string{
		"", "a", "_", "z9", "_9", "orders", "order_lines", "a_b_c_0123456789",
		"9a", "A", "aB", "a-b", "a b", "a.b", "é", "a\n", "\na", "a;", "sqlite_master", "{", "`", "/",
	} {
		if got, want := isTableName(name), shape.MatchString(name); got != want {
			t.Errorf("isTableName(%q) = %v, want %v", name, got, want)
		}
	}
}
