module github.com/kitsunium/sdk

go 1.27.3

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

retract [v0.18.0, v0.19.7] // these held pkg/ and internal/, now a module per pkg/v1 family and github.com/kitsunium/sdk/internal (ADR 0171): go get github.com/kitsunium/sdk@latest && go mod tidy

require (
	github.com/kitsunium/sdk/internal v0.26.44
	github.com/kitsunium/sdk/internal/core v0.18.0
	github.com/kitsunium/sdk/internal/kernel v0.18.0
	github.com/kitsunium/sdk/internal/service v0.18.0
	github.com/kitsunium/sdk/pkg/v1/app v0.16.44
	github.com/kitsunium/sdk/pkg/v1/clock v0.1.2
	github.com/kitsunium/sdk/pkg/v1/concur v0.1.5
	github.com/kitsunium/sdk/pkg/v1/crypto v0.3.3
	github.com/kitsunium/sdk/pkg/v1/data v0.11.2
	github.com/kitsunium/sdk/pkg/v1/errs v0.2.4
	github.com/kitsunium/sdk/pkg/v1/net v0.5.25
	github.com/kitsunium/sdk/pkg/v1/observe v0.4.20
	github.com/kitsunium/sdk/pkg/v1/proc v0.2.10
	github.com/kitsunium/sdk/pkg/v1/security v0.4.7
)
