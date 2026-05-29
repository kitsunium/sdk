// Package writer — credential value + provider port shared by the network
// writers (s3, cloudwatch). Carries no AWS types; the redacting CredentialValue
// keeps secrets out of any accidental log emission (rule 4 Public/Private).
package writer

import "context"

// CredentialProvider yields short-lived credentials on demand. The SDK never
// stores, logs, or embeds the returned material in an errs Field; it is passed
// straight to the underlying transport client. Consumers implement this to plug
// in IAM roles, STS, Vault, or static keys without exposing the secret to the
// logging pipeline.
type CredentialProvider interface {
	// Credentials returns the current credential set, or an error when it
	// cannot be obtained (expired role, unreachable STS, …).
	Credentials(ctx context.Context) (creds CredentialValue, err error)
}

// CredentialValue is an opaque, redacting AWS SigV4 credential set. Its String
// output is always "<redacted>" so an accidental %v / %s never leaks the
// secret; the typed accessors expose the material only to the transport factory
// that explicitly asks for it.
type CredentialValue struct {
	// accessKeyID / secretAccessKey / sessionToken hold SigV4 material; all
	// unexported so the only way out is the explicit accessors below.
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
}

// NewCredentialValue builds a CredentialValue from SigV4 material. An empty
// sessionToken is valid for long-lived keys; supply it for STS / assumed roles.
func NewCredentialValue(accessKeyID, secretAccessKey, sessionToken string) CredentialValue {
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
