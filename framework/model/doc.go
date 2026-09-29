// A doc comment's default text and its translations.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// SplitDoc splits a description into its default text and its translations.
//
// A product documents a building block once, in Go, in its default language;
// a paragraph that starts with a language tag and a colon — "fr: Le cycle
// de vie d'un compte…" — is the same description in that language, until
// the next tag or the end:
//
//	// AccountLifecycle is the life of an account…
//	//
//	// fr: AccountLifecycle est la vie d'un compte…
//
// It keeps Go's convention (the comment still starts with the name) and
// go doc prints both. Each text is joined into one line. A description whose
// only tagged paragraph is "en" uses it as the default.
func SplitDoc(text string) (string, map[string]string) {
	return core.SplitDoc(text)
}
