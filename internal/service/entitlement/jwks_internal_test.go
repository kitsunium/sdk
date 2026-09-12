package entitlement

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// Test_signsRS256 pins which published entries are allowed to verify a
// signature at all.
//
// "use" and "alg" are optional in JWK and GitHub has published both shapes, so
// an entry declaring neither must be accepted. What must not be is an entry
// declaring something else: a key published for encryption, or for another
// algorithm, is being repurposed for a job it was not published for.
func Test_signsRS256(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry JWKValue
		want  bool
	}{
		{name: "fully declared", entry: JWKValue{KeyType: "RSA", Use: "sig", Algorithm: "RS256"}, want: true},
		{name: "no use, no alg", entry: JWKValue{KeyType: "RSA"}, want: true},
		{name: "use only", entry: JWKValue{KeyType: "RSA", Use: "sig"}, want: true},
		{name: "an encryption key", entry: JWKValue{KeyType: "RSA", Use: "enc"}},
		{name: "another algorithm", entry: JWKValue{KeyType: "RSA", Algorithm: "PS256"}},
		{name: "an EC key", entry: JWKValue{KeyType: "EC", Use: "sig", Algorithm: "RS256"}},
		{name: "no key type", entry: JWKValue{Use: "sig"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := signsRS256(&tt.entry); got != tt.want {
				t.Errorf("signsRS256(%+v) = %v, want %v", tt.entry, got, tt.want)
			}
		})
	}
}

// Test_ParseJWKSInternals pins the set-level refusals from inside the package,
// where a malformed document can be handed in directly rather than round-tripped
// through a struct that could not express it.
func Test_ParseJWKSInternals(t *testing.T) {
	t.Parallel()

	priv, err := rsa.GenerateKey(nil, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	usable := JWKValue{
		KeyType:   "RSA",
		KeyID:     "k1",
		Use:       "sig",
		Algorithm: "RS256",
		Modulus:   base64.RawURLEncoding.EncodeToString(priv.N.Bytes()),
		Exponent:  base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes()),
	}
	encoded, marshalErr := json.Marshal(JWKSValue{Keys: []JWKValue{usable}})
	if marshalErr != nil {
		t.Fatalf("marshalling key set: %v", marshalErr)
	}

	tests := []struct {
		name    string
		raw     string
		wantErr bool
		reason  string
	}{
		{name: "a usable set", raw: string(encoded)},
		{name: "not json", raw: "<html>not a key set</html>", wantErr: true, reason: "a captive portal must not read as zero keys"},
		{
			name:    "an empty set",
			raw:     `{"keys":[]}`,
			wantErr: true,
			reason:  "returning no keys reports every token as an unknown kid, sending the caller refreshing forever",
		},
		{
			//: The entry-count limit below only applies once json.Unmarshal has
			//: already built the whole document in memory, which is too late
			//: to be a limit at all.
			name:    "a document larger than any real key set",
			raw:     `{"keys":[],"pad":"` + strings.Repeat("A", maxJWKSBytes) + `"}`,
			wantErr: true,
			reason:  "size must be refused before decoding, not after",
		},
		{
			name:    "more keys than a real set carries",
			raw:     `{"keys":[` + strings.Repeat(`{"kty":"oct","kid":"x"},`, maxJWKSKeys) + `{"kty":"oct","kid":"y"}]}`,
			wantErr: true,
			reason:  "an oversized set makes key selection expensive for no reason",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, parseErr := ParseJWKS([]byte(tt.raw))
			if tt.wantErr {
				if !errors.Is(parseErr, coreent.ErrCIUnverifiable) {
					t.Errorf("ParseJWKS() error = %v, want coreent.ErrCIUnverifiable (%s)", parseErr, tt.reason)
				}
				return
			}
			if parseErr != nil {
				t.Errorf("ParseJWKS() error = %v, want nil", parseErr)
			}
		})
	}
}
