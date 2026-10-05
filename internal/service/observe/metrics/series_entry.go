package metrics

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// seriesEntry binds one instrument instance to the identity it was created
// under. name and attrs are written once at creation and never mutated, which
// is what lets Collect hand the attribute slice straight to the snapshot
// instead of cloning one slice per series per collection.
type seriesEntry[T any] struct {
	name  string
	attrs []coreotel.AttrValue
	inst  T
}
