// Internal test for the end-to-end shape of the null-device exclusion.
package selfupdate

import (
	"os"
	"testing"
)

// TestStdinFromNullIsNotATerminal pins the whole path the finding described:
// stdin redirected from /dev/null must read as unattended.
//
// It is the case a CI script writes by hand — `tool upgrade < /dev/null` — and
// it passed the mode test, so the consent path wrote a prompt to a reader that
// answers EOF forever instead of refusing immediately.
//
// Not parallel: it swaps os.Stdin, which is process-wide.
func TestStdinFromNullIsNotATerminal(t *testing.T) {
	tests := []struct{ name string }{{name: "stdin redirected from the null device"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			null, err := os.Open(nullDevicePath)
			//: A platform without /dev/null has nothing to assert here.
			if err != nil {
				t.Skipf("%s unavailable: %v", nullDevicePath, err)
			}
			defer func() { _ = null.Close() }()

			original := os.Stdin
			os.Stdin = null
			defer func() { os.Stdin = original }()

			//: Nobody is behind the null device, whatever its mode says.
			if StdinIsTerminal() {
				t.Error("StdinIsTerminal() = true with stdin on the null device, want false")
			}
		})
	}
}
