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
	tests := []struct {
		name string
		val  any
		want string
	}{
		{"string value renders verbatim", "hello", "hello"},
		{"int value renders as decimal", 42, "42"},
		{"boolean value renders as keyword", true, "true"},
		{"nil value renders as <nil>", nil, "<nil>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := safeString(tc.val); got != tc.want {
				t.Errorf("safeString(%v) = %q, want %q", tc.val, got, tc.want)
			}
		})
	}
}

// Test_safeTypeName asserts the type-renderer contract: %T renders the Go
// type without invoking user code, so it cannot panic even on pathological
// values (panicking String / Error methods).
func Test_safeTypeName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		val           any
		wantSubstring string
	}{
		{"string value renders as string", "hello", "string"},
		{"int value renders as int", 42, "int"},
		{"nil value renders as <nil>", nil, "<nil>"},
		{"evil stringer renders its type, not its String()", evilStringer{}, "evilStringer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := safeTypeName(tc.val)
			if !strings.Contains(got, tc.wantSubstring) {
				t.Errorf("safeTypeName(%v) = %q, want substring %q", tc.val, got, tc.wantSubstring)
			}
		})
	}
}

// evilStringer has a String() method that panics. safeString MUST degrade
// gracefully when rendering such a value — otherwise a meta-panic inside
// the deferred recover block would crash the producer goroutine.
type evilStringer struct{}

// String panics to simulate a pathological type reaching the recover sink.
func (evilStringer) String() (s string) {
	//: deliberately panic to probe the inner recover in safeString.
	panic("evil: String() panic")
}

// nestedPanicPayload is a panic value whose own String() panics. fmt's
// internal Stringer-panic guard catches the OUTER panic, then tries to
// render this payload, panics AGAIN, and re-raises rather than producing a
// "%!v(PANIC=...)" marker — the only path that drives safeString's inner
// recover (panic_value.go:37) instead of letting fmt absorb the panic.
type nestedPanicPayload struct{}

// String panics so fmt cannot format the payload during its own panic guard.
func (nestedPanicPayload) String() string {
	//: panic during payload formatting forces fmt to re-raise.
	panic("nested: payload String() panic")
}

// doublePanicStringer panics with a value that ITSELF panics on String().
// This re-raises out of fmt.Sprintf, exercising safeString's inner recover.
type doublePanicStringer struct{}

// String panics with a nestedPanicPayload so fmt re-raises the meta-panic.
func (doublePanicStringer) String() string {
	//: panic payload is itself unformattable, defeating fmt's panic guard.
	panic(nestedPanicPayload{})
}

// Test_safeString_GuardsAgainstPanickingStringer asserts the meta-panic
// invariant: even when v.String() panics, safeString returns gracefully.
//
// Two acceptable degraded renderings:
//   - fmt.Sprintf's own "%!v(PANIC=...)" marker (fmt catches Stringer panics
//     internally before our inner recover ever triggers — happy path today).
//   - safeString's "<panic in String(): T>" marker (triggered only if a
//     non-Stringer path inside fmt leaks a panic — defence-in-depth).
func Test_safeString_GuardsAgainstPanickingStringer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: val is the input to safeString; the panicking variant exercises the
		//: meta-panic guard while normal values prove the happy path.
		val any
		//: degradedMarker is true when the result MUST include one of the
		//: known degraded markers ("PANIC=" or "panic in String()").
		//: false cases assert the happy-path rendering (no degraded marker).
		degradedMarker bool
	}{
		{"evil stringer triggers a degraded marker", evilStringer{}, true},
		{"benign string does not surface a degraded marker", "hello", false},
		{"benign int does not surface a degraded marker", 42, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				//: outer test must not observe a panic — safeString absorbs it.
				if r := recover(); r != nil {
					t.Fatalf("safeString allowed a meta-panic to escape: %v", r)
				}
			}()
			got := safeString(tc.val)
			okFmt := strings.Contains(got, "PANIC=")
			okInner := strings.Contains(got, "panic in String()")
			gotDegraded := okFmt || okInner
			if gotDegraded != tc.degradedMarker {
				t.Errorf("safeString(%v) = %q, gotDegraded=%v want %v", tc.val, got, gotDegraded, tc.degradedMarker)
			}
		})
	}
}

// Test_safeString_InnerRecoverMarker drives the meta-panic arm that fmt
// cannot absorb: a Stringer whose panic payload is itself unformattable
// forces fmt.Sprintf to re-raise, so safeString's own deferred recover
// (panic_value.go:37) substitutes the "<panic in String(): T>" marker.
// The plain evilStringer case is absorbed by fmt and therefore exercises a
// different arm — this test pins the genuine inner-recover path.
func Test_safeString_InnerRecoverMarker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  any
	}{
		{"double-panic stringer triggers safeString's inner recover", doublePanicStringer{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				//: the inner recover must absorb the re-raised meta-panic.
				if r := recover(); r != nil {
					t.Fatalf("safeString allowed a meta-panic to escape: %v", r)
				}
			}()
			got := safeString(tc.val)
			//: the substituted marker proves line 37 ran (not fmt's own guard).
			if !strings.Contains(got, "panic in String()") {
				t.Errorf("safeString(%v) = %q, want inner-recover marker %q", tc.val, got, "<panic in String(): T>")
			}
		})
	}
}
