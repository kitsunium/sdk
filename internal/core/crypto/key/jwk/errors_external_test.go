package jwk_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto/key/jwk"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// jwkRange is the 0.3.42.* block with its serial byte cleared.
const jwkRange errs.Code = 0x00_03_2A_00

// Test_sentinels pins what moved here from internal/service/crypto/key/jwk
// (ADR 0160): each sentinel carries the code and the reason it carried before
// the move, inside the range the service layer allocated. A code is a wire
// contract a dashboard branches on, so a value that drifted while its
// declaration moved is the one regression this package can have.
func Test_sentinels(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		sentinel *errs.Error
		code     errs.Code
		reason   string
	}
	tests := []tc{
		{"a malformed document", jwk.Malformed, jwk.CodeJWKMalformed, "MALFORMED"},
		{"a missing member", jwk.MissingMember, jwk.CodeJWKMissingMember, "MISSING_MEMBER"},
		{"an unsupported key type", jwk.UnsupportedKeyType, jwk.CodeJWKUnsupportedKeyType, "UNSUPPORTED_KEY_TYPE"},
		{"an unsupported curve", jwk.UnsupportedCurve, jwk.CodeJWKUnsupportedCurve, "UNSUPPORTED_CURVE"},
		{"an invalid encoding", jwk.InvalidEncoding, jwk.CodeJWKInvalidEncoding, "INVALID_ENCODING"},
		{"a key mismatch", jwk.KeyMismatch, jwk.CodeJWKKeyMismatch, "KEY_MISMATCH"},
		{"no public form", jwk.NoPublicForm, jwk.CodeJWKNoPublicForm, "NO_PUBLIC_FORM"},
		{"no private material", jwk.NoPrivateMaterial, jwk.CodeJWKNoPrivateMaterial, "NO_PRIVATE_MATERIAL"},
		{"a type mismatch", jwk.TypeMismatch, jwk.CodeJWKTypeMismatch, "TYPE_MISMATCH"},
		{"a key not found", jwk.KeyNotFound, jwk.CodeJWKKeyNotFound, "KEY_NOT_FOUND"},
		{"an ambiguous kid", jwk.AmbiguousKid, jwk.CodeJWKAmbiguousKid, "AMBIGUOUS_KID"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the code is the one the service declared, unchanged by the move.
		if got := c.sentinel.Code(); got != c.code {
			t.Errorf("Code() = %v, want %v", got, c.code)
		}
		//: and it stays inside the range codeRangeOwners gives this package.
		if got := c.code &^ 0xFF; got != jwkRange {
			t.Errorf("code %v is outside 0.3.42.*", c.code)
		}
		//: the reason is what a log query matches on, so it did not move either.
		if got := c.sentinel.Reason(); got != c.reason {
			t.Errorf("Reason() = %q, want %q", got, c.reason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
