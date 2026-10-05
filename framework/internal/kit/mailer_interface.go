package kit

// revealStringer is a secret that shows its value, which parsing a URL needs.
type revealStringer interface {
	RevealString() string
}
