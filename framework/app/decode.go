package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// DefaultMaxBody is the largest request body an endpoint reads, unless the
// endpoint says otherwise with [MaxBody].
const DefaultMaxBody int64 = iapp.DefaultMaxBody
