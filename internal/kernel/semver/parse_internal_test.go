package semver

import "testing"

// TestByteClassesMatchTheirDefinitions checks the one-comparison forms of
// isDigit and isLetter against the plain ranges they stand for, over every
// byte there is. The forms rely on unsigned wrap-around and on bit 0x20
// separating the cases — tricks a reader should not have to take on trust.
func TestByteClassesMatchTheirDefinitions(t *testing.T) {
	t.Parallel()
	for i := range 256 {
		c := byte(i)
		if want := '0' <= c && c <= '9'; isDigit(c) != want {
			t.Errorf("isDigit(%#x) = %v, want %v", c, isDigit(c), want)
		}
		if want := ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z'); isLetter(c) != want {
			t.Errorf("isLetter(%#x) = %v, want %v", c, isLetter(c), want)
		}
	}
}
