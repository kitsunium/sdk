// Package kit — what building an SMTP transport needs of its URL.
package kit

// revealStringer is a secret that shows its value, which parsing a URL needs.
type revealStringer interface {
	RevealString() string
}
