package recover

import (
	"strings"
	"testing"
)

// Test_panicValue_Error asserts the type-only rendering rule: Error() MUST
// expose only the Go type of the panic value — never the verbatim stringified
// value, which may carry internal state. The rich %v rendering belongs in
// the wrapping *errs.Error's Private field (via safeString in recover_sink).
func Test_panicValue_Error(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  any
		//: want is a substring that Error() MUST contain (type token).
		wantSubstring string
		//: forbidden is a substring that Error() MUST NOT contain (value).
		forbiddenSubstring string
	}{
		{"string panic value", "boom", "string", "boom"},
		{"int panic value", 42, "int", "42"},
		{"nil panic value", nil, "<nil>", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := panicValue{v: tc.val}
			got := p.Error()
			//: type token must appear — downstream loggers rely on it to
			//: distinguish panic origin without leaking value content.
			if !strings.Contains(got, tc.wantSubstring) {
				t.Errorf("Error() = %q, want type token %q", got, tc.wantSubstring)
			}
			//: value content must NOT appear — anti-leak invariant.
			if tc.forbiddenSubstring != "" && strings.Contains(got, tc.forbiddenSubstring) {
				t.Errorf("Error() = %q leaks value content %q via Source() chain", got, tc.forbiddenSubstring)
			}
		})
	}
}

// Test_safeString_RendersValue covers the happy path of safeString.
func Test_safeString_RendersValue(t *testing.T) {
	t.Parallel()
	if got := safeString("hello"); got != "hello" {
		t.Errorf("safeString(\"hello\") = %q, want \"hello\"", got)
	}
	if got := safeString(42); got != "42" {
		t.Errorf("safeString(42) = %q, want \"42\"", got)
	}
}

// evilStringer has a String() method that panics. safeString MUST degrade
// gracefully when rendering such a value — otherwise a meta-panic inside
// the deferred recover block would crash the producer goroutine.
type evilStringer struct{}

// String panics to simulate a pathological type reaching the recover sink.
//
// Returns:
//   - s: never returned; always panics.
func (evilStringer) String() (s string) {
	//: deliberately panic to probe the inner recover in safeString.
	panic("evil: String() panic")
}

// Test_safeString_GuardsAgainstPanickingStringer asserts the meta-panic
// invariant: even when v.String() panics, safeString returns gracefully.
// Regresses finding #25 from post-audit review.
//
// Two acceptable degraded renderings:
//   - fmt.Sprintf's own "%!v(PANIC=...)" marker (fmt catches Stringer panics
//     internally before our inner recover ever triggers — happy path today).
//   - safeString's "<panic in String(): T>" marker (triggered only if a
//     non-Stringer path inside fmt leaks a panic — defence-in-depth).
func Test_safeString_GuardsAgainstPanickingStringer(t *testing.T) {
	t.Parallel()
	defer func() {
		//: the outer test must not observe a panic — safeString must absorb it.
		if r := recover(); r != nil {
			t.Fatalf("safeString allowed a meta-panic to escape: %v", r)
		}
	}()
	got := safeString(evilStringer{})
	//: accept either degraded marker — both prove the producer survives.
	okFmt := strings.Contains(got, "PANIC=")
	okInner := strings.Contains(got, "panic in String()")
	if !okFmt && !okInner {
		t.Errorf("safeString on evilStringer = %q, want a degraded-rendering marker", got)
	}
}
