// Package listen — descriptor names (#739).
package listen

import (
	"errors"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// TestCheckName pins systemd's fdname_is_valid: at most 255 bytes, printable
// ASCII, no ':'; the empty name valid, announced as "unknown".
func TestCheckName(t *testing.T) {
	t.Parallel()
	valid := []string{"", "web", "a b", "stored", "~!@#", strings.Repeat("n", 255)}
	invalid := []string{"a:b", ":", "tab\there", "nl\n", "del\x7f", "é", strings.Repeat("n", 256)}
	for _, name := range valid {
		if err := CheckName(name); err != nil {
			t.Errorf("CheckName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range invalid {
		if err := CheckName(name); !errors.Is(err, coreproc.InvalidFDName) {
			t.Errorf("CheckName(%q) = %v, want InvalidFDName", name, err)
		}
	}
	if announced("") != "unknown" || announced("web") != "web" {
		t.Errorf("announced: %q %q", announced(""), announced("web"))
	}
}
