module github.com/kitsunium/sdk

go 1.27.1

// The root module is the workspace's anchor: nothing requires it, and the
// release chain leaves it out, so it is never tagged. Every vendor integration
// under third-party/ is a module of its own, in that chain (ADR 0157) —
// third-party/aws, third-party/codec/{hcl,protobuf},
// third-party/db/writer/{clickhouse,mysql,redis}, third-party/transform and
// third-party/x-crypto. What is left here is entitlement's ssh Identity
// (third-party/entitlement, ADR 0079). The Docker-backed integration suites
// live in the auxiliary e2e module (e2e/integration), outside go.work.
require (
	github.com/kitsunium/sdk/internal/core v0.1.16
	github.com/kitsunium/sdk/internal/kernel v0.1.16
	github.com/kitsunium/sdk/internal/service v0.1.16
	golang.org/x/crypto v0.55.0
)

require (
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/kitsunium/sdk/internal/kernel => ./internal/kernel

replace github.com/kitsunium/sdk/internal/core => ./internal/core

replace github.com/kitsunium/sdk/internal/service => ./internal/service
