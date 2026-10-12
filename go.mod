module github.com/kitsunium/sdk

go 1.27.2

// The SDK's root module (ADR 0171): it holds the framework under framework/,
// and nothing of internal/ or pkg/ — each pkg/v1/<family> is a module of its
// own, and internal/ (kernel, core, service) is one shared module that Go's
// internal/ rule keeps every consumer out of. Import paths are unchanged:
// github.com/kitsunium/sdk/pkg/v1/... and github.com/kitsunium/sdk/framework/....
// A consumer requires the family modules it imports, each at its own version.
//
// The modules that require a vendor stay modules of their own, so a consumer of
// one integration takes that vendor's graph and no other: the nine under
// third-party/ (ADR 0157) and the four framework connectors under
// framework/connectors/ (ADR 0147, ADR 0158). Every module is tagged
// <dir>/vX.Y.Z — the root vX.Y.Z — only by a release that changes it. The
// replace directives point each repository module at its directory for a
// GOWORK=off build; the release drops them and pins the versions it cuts
// (scripts/release/cut-tags.sh). The Docker-backed integration suites live in
// the auxiliary e2e module, outside go.work with tools/genindex and
// tools/sdkguard.
//
// v0.18.0 to v0.19.7 held pkg/ and internal/ in this module, so a build that
// required one of them beside a family module would meet an ambiguous import:
// they are retracted, and every family and internal require this module at
// the first version that holds neither (written by the release, ADR 0171 §7).

retract [v0.18.0, v0.19.7] // pkg/v1/<family> and internal moved to modules of their own (ADR 0171): go get github.com/kitsunium/sdk@latest and the github.com/kitsunium/sdk/pkg/v1/<family> modules you import

replace github.com/kitsunium/sdk/internal => ./internal

replace github.com/kitsunium/sdk/pkg/v1/app => ./pkg/v1/app

replace github.com/kitsunium/sdk/pkg/v1/clock => ./pkg/v1/clock

replace github.com/kitsunium/sdk/pkg/v1/concur => ./pkg/v1/concur

replace github.com/kitsunium/sdk/pkg/v1/crypto => ./pkg/v1/crypto

replace github.com/kitsunium/sdk/pkg/v1/data => ./pkg/v1/data

replace github.com/kitsunium/sdk/pkg/v1/errs => ./pkg/v1/errs

replace github.com/kitsunium/sdk/pkg/v1/net => ./pkg/v1/net

replace github.com/kitsunium/sdk/pkg/v1/observe => ./pkg/v1/observe

replace github.com/kitsunium/sdk/pkg/v1/proc => ./pkg/v1/proc

replace github.com/kitsunium/sdk/pkg/v1/security => ./pkg/v1/security

require (
	github.com/kitsunium/sdk/internal v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/app v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/clock v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/concur v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/crypto v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/data v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/errs v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/net v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/observe v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/proc v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/security v0.0.0-00010101000000-000000000000
)
