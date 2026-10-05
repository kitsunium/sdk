package i18n

import corei18n "github.com/kitsunium/sdk/internal/core/app/i18n"

// The three interfaces a Store satisfies.
var (
	_ corei18n.Catalog    = (*Store)(nil)
	_ corei18n.KeyLister  = (*Store)(nil)
	_ corei18n.Fallbacker = (*Store)(nil)
)
