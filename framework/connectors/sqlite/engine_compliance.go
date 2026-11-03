package sqlite

import "github.com/kitsunium/sdk/framework/app"

// The engine reads its driver's errors: a compile-time assertion that it is
// the Classifier app.Transact asks whether a failed transaction runs again
// (#1000), so the build fails before any test runs if the two drift apart.
var _ app.Classifier = engine{}
