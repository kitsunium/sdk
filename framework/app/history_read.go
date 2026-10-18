package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Former is one former value of a field that keeps its history: what it was,
// until when, and who replaced it.
type Former = iapp.Former
