package writer

// newCredentialValue is NewCredentialValue's body: decl_gen.go writes NewCredentialValue, from the
// design, as one call of it.
func newCredentialValue(accessKeyID, secretAccessKey, sessionToken string) CredentialValue {
	//: store the SigV4 material behind the redacting value.
	return CredentialValue{
		accessKeyID:     accessKeyID,
		secretAccessKey: secretAccessKey,
		sessionToken:    sessionToken,
	}
}

// AccessKeyID returns the AWS access key ID (may be empty).
func (c CredentialValue) AccessKeyID() string {
	//: plain accessor — the redaction is only for String / log emission.
	return c.accessKeyID
}

// SecretAccessKey returns the AWS secret access key (may be empty). Callers
// MUST NOT log the result.
func (c CredentialValue) SecretAccessKey() string {
	//: plain accessor — handed straight to the SigV4 signer, never logged.
	return c.secretAccessKey
}

// SessionToken returns the AWS session token (empty for long-lived keys).
func (c CredentialValue) SessionToken() string {
	//: plain accessor — empty unless STS / assumed-role credentials were used.
	return c.sessionToken
}

// String implements fmt.Stringer and always redacts so the secret never reaches
// a log line through %v / %s.
func (c CredentialValue) String() string {
	//: constant redaction marker regardless of which fields are populated.
	return "<redacted>"
}

// goString is CredentialValue.GoString's body: decl_gen.go writes CredentialValue.GoString, from the
// design, as one call of it.
func (c CredentialValue) goString() string {
	//: same constant marker — %#v must never expose credential material.
	return "<redacted>"
}
