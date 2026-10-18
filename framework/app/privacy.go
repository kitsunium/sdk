package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Privacy is the framework's own service: the legal holds and the privacy journal of
// an app that keeps personal data. The framework mounts a copy of it — named
// app.privacy, a name no service of a product can take — in every app whose
// stores keep personal data or declare a retention, before the product's
// services. Its stores stay in the data directory unless the product places
// them (ADR 0004). A product never mounts it itself.
var Privacy *Service = iapp.Privacy
