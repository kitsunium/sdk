package token_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestNewClaimsBufRefusesBadDeclarations pins every refusal of a declaration.
func TestNewClaimsBufRefusesBadDeclarations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		names []string
		want  errs.Code
	}{
		{"empty", []string{""}, token.CodeClaimNameInvalid},
		{"registered", []string{"sub"}, token.CodeClaimNameInvalid},
		{"repeated", []string{"a", "a"}, token.CodeClaimNameInvalid},
		{"quote", []string{`a"b`}, token.CodeClaimNameInvalid},
		{"backslash", []string{`a\b`}, token.CodeClaimNameInvalid},
		{"control", []string{"a\nb"}, token.CodeClaimNameInvalid},
		{"not UTF-8", []string{"a\xffb"}, token.CodeClaimNameInvalid},
		{"too long", []string{strings.Repeat("n", token.ClaimsBufTextCap+1)}, token.CodeClaimsBufOverflow},
		{"too many", make([]string, token.ClaimsBufPrivateCap+1), token.CodeClaimsBufOverflow},
	}
	//: each refusal on its own.
	for _, tc := range cases {
		buf, err := token.NewClaimsBuf(tc.names...)
		//: refused, and no half-declared buffer.
		if !errs.HasCode(err, tc.want) || buf != nil {
			t.Fatalf("%s: NewClaimsBuf = %v, %v; want code %v", tc.name, buf, err, tc.want)
		}
	}
	buf, err := token.NewClaimsBuf("name", "role")
	//: a valid declaration.
	if err != nil || buf.Declared() != 2 || buf.PrivateIndex([]byte("role")) != 1 || buf.PrivateIndex([]byte("x")) != -1 {
		t.Fatalf("NewClaimsBuf(name, role) = %v, %v", buf, err)
	}
}

// TestClaimsBufNeverTruncates stores values at and past every capacity: at
// the bound a value is kept whole, past it the claim is refused and absent.
func TestClaimsBufNeverTruncates(t *testing.T) {
	t.Parallel()
	buf, _ := token.NewClaimsBuf("name")
	full := []byte(strings.Repeat("f", token.ClaimsBufTextCap))
	//: exactly the capacity fits.
	if err := buf.SetText(token.TextSubject, full); err != nil {
		t.Fatalf("SetText at capacity: %v", err)
	}
	//: one more octet is refused, and the claim is left absent.
	if err := buf.SetText(token.TextSubject, append(full, 'x')); !errs.HasCode(err, token.CodeClaimsBufOverflow) {
		t.Fatalf("SetText past capacity = %v", err)
	}
	//: absent, not cut.
	if value, state := buf.Text(token.TextSubject); value != nil || state != token.ClaimAbsent {
		t.Fatalf("after an overflow: %q %v", value, state)
	}
	//: the audience count is a bound too.
	for range token.ClaimsBufAudienceCap {
		//: each member fits.
		if err := buf.AppendAudience([]byte("a")); err != nil {
			t.Fatalf("AppendAudience: %v", err)
		}
	}
	//: one member too many.
	if err := buf.AppendAudience([]byte("a")); !errs.HasCode(err, token.CodeClaimsBufOverflow) {
		t.Fatalf("AppendAudience past capacity = %v", err)
	}
	//: an undeclared index has no slot.
	if err := buf.SetPrivate(1, []byte("v")); !errs.HasCode(err, token.CodeClaimsBufOverflow) {
		t.Fatalf("SetPrivate on an undeclared index = %v", err)
	}
	//: the empty value is present but invalid.
	if err := buf.SetPrivate(0, nil); err != nil {
		t.Fatalf("SetPrivate(empty): %v", err)
	}
	//: invalid, with no value.
	if value, state := buf.Private("name"); value != nil || state != token.ClaimInvalid {
		t.Fatalf("empty private: %q %v", value, state)
	}
}

// TestClaimsBufShortValueLeavesNoTail stores a long value then a short one in
// the same slot: the view is the short value, and the octets past it are gone.
func TestClaimsBufShortValueLeavesNoTail(t *testing.T) {
	t.Parallel()
	buf, _ := token.NewClaimsBuf()
	_ = buf.SetText(token.TextIssuer, []byte("a-long-issuer-name"))
	_ = buf.SetText(token.TextIssuer, []byte("ab"))
	value, _ := buf.Text(token.TextIssuer)
	//: the view stops at the value, and cannot be grown into the slot.
	if string(value) != "ab" || cap(value) != 2 {
		t.Fatalf("view = %q cap %d", value, cap(value))
	}
	//: no tail: the octets past the short value were cleared.
	if extended := value[:cap(value)]; string(extended) != "ab" {
		t.Fatalf("tail = %q", extended)
	}
	buf.Reset()
	//: reset leaves nothing usable.
	if value, state := buf.Text(token.TextIssuer); value != nil || state != token.ClaimAbsent {
		t.Fatalf("after Reset: %q %v", value, state)
	}
}

// TestScratchRedactsAndGuards checks the scratch's own contract: Enter by
// owner, MACEqual only while entered, and a shape-only rendering.
func TestScratchRedactsAndGuards(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef0123456789abcdef")
	owner := token.NewScratchOwner()
	scratch := token.NewVerifyScratch(owner, hmac.New(sha256.New, key))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("input"))
	tag := mac.Sum(nil)
	//: not entered: no verdict but false.
	if scratch.MACEqual([]byte("input"), tag) {
		t.Fatalf("MACEqual on a scratch nobody entered reported true")
	}
	//: another owner is foreign.
	if err := scratch.Enter(token.NewScratchOwner()); !errs.HasCode(err, token.CodeScratchForeign) {
		t.Fatalf("Enter(other owner) = %v", err)
	}
	//: the owner enters.
	if err := scratch.Enter(owner); err != nil {
		t.Fatalf("Enter(owner) = %v", err)
	}
	//: twice is busy.
	if err := scratch.Enter(owner); !errs.HasCode(err, token.CodeScratchBusy) {
		t.Fatalf("second Enter = %v", err)
	}
	//: the right tag, then a wrong one, on the same scratch.
	if !scratch.MACEqual([]byte("input"), tag) || scratch.MACEqual([]byte("other"), tag) {
		t.Fatalf("MACEqual verdicts are wrong")
	}
	rendered := fmt.Sprintf("%v %+v %#v %s", scratch, scratch, scratch, scratch)
	//: the shape only.
	if strings.Contains(rendered, fmt.Sprintf("%x", key)) || !strings.Contains(rendered, "busy:true") {
		t.Fatalf("rendering = %s", rendered)
	}
	scratch.Leave()
	//: released.
	if got := scratch.String(); got != "token.VerifyScratch{owned:true busy:false}" {
		t.Fatalf("String = %s", got)
	}
	var none *token.VerifyScratch
	//: nil is foreign, and renders.
	if err := none.Enter(owner); !errs.HasCode(err, token.CodeScratchForeign) || none.String() != "token.VerifyScratch{nil}" {
		t.Fatalf("nil scratch: %v %s", err, none.String())
	}
}

// TestClaimStateNames pins the four spellings.
func TestClaimStateNames(t *testing.T) {
	t.Parallel()
	//: every state, and one past them.
	for state, want := range map[token.ClaimState]string{
		token.ClaimAbsent: "absent", token.ClaimWrongType: "wrong_type",
		token.ClaimInvalid: "invalid", token.ClaimOK: "ok", token.ClaimOK + 1: "unknown",
	} {
		//: one spelling each.
		if got := state.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", state, got, want)
		}
	}
}
