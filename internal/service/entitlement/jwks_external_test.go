package entitlement_test

import (
	"crypto/rsa"
	"encoding/json"
	"errors"
	"testing"

	entitlement "github.com/kitsunium/sdk/internal/service/entitlement"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// TestParseJWKS pins which published keys are usable, and which sets are
// refused outright.
func TestParseJWKS(t *testing.T) {
	t.Parallel()

	priv, err := rsa.GenerateKey(nil, testKeyBits)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	small, err := rsa.GenerateKey(nil, undersizedKeyBits)
	if err != nil {
		t.Fatalf("generating small key: %v", err)
	}

	tests := []struct {
		name    string
		keys    []entitlement.JWKValue
		wantLen int
		wantErr bool
		reason  string
	}{
		{
			name:    "a published signing key",
			keys:    []entitlement.JWKValue{jwkFor(priv, "k1")},
			wantLen: 1,
		},
		{
			name:    "an entry with no kid is skipped",
			keys:    []entitlement.JWKValue{jwkFor(priv, "k1"), jwkFor(priv, "")},
			wantLen: 1,
			reason:  "a token cannot select it, so it could only be reached by trying every key",
		},
		{
			name:    "an unusable entry does not sink the set",
			keys:    []entitlement.JWKValue{jwkFor(priv, "k1"), {KeyType: "EC", KeyID: "k2"}},
			wantLen: 1,
			reason:  "refusing the whole set over one unrelated key type would take down every CI seat",
		},
		{
			name:    "a repeated kid is refused",
			keys:    []entitlement.JWKValue{jwkFor(priv, "k1"), jwkFor(priv, "k1")},
			wantErr: true,
			reason:  "an ambiguous trust anchor is not one",
		},
		{
			name:    "an undersized key is refused",
			keys:    []entitlement.JWKValue{jwkFor(small, "k1")},
			wantErr: true,
			reason:  "a key too small to be GitHub's has been substituted somewhere",
		},
		{
			name:    "a set with no usable key is refused",
			keys:    []entitlement.JWKValue{{KeyType: "EC", KeyID: "k2"}},
			wantErr: true,
			reason:  "silently returning an empty set reports every token as an unknown kid",
		},
		{
			name:    "an encryption key is not a signing key",
			keys:    []entitlement.JWKValue{{KeyType: "RSA", KeyID: "k1", Use: "enc", Modulus: "AQAB", Exponent: "AQAB"}},
			wantErr: true,
			reason:  "a key published for encryption is being repurposed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, marshalErr := json.Marshal(entitlement.JWKSValue{Keys: tt.keys})
			if marshalErr != nil {
				t.Fatalf("marshalling key set: %v", marshalErr)
			}

			got, parseErr := entitlement.ParseJWKS(raw)
			if tt.wantErr {
				if !errors.Is(parseErr, coreent.ErrCIUnverifiable) {
					t.Errorf("ParseJWKS() error = %v, want coreent.ErrCIUnverifiable (%s)", parseErr, tt.reason)
				}
				return
			}
			if parseErr != nil {
				t.Fatalf("ParseJWKS() error = %v, want nil", parseErr)
			}
			if len(got) != tt.wantLen {
				t.Errorf("ParseJWKS() built %d keys, want %d (%s)", len(got), tt.wantLen, tt.reason)
			}
		})
	}
}
