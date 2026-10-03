module github.com/kitsunium/sdk

go 1.27.1

// The root module is the workspace's anchor: nothing requires it, and the
// release chain leaves it out, so it is never tagged. Every vendor integration
// under third-party/ is a module of its own, in that chain (ADR 0157) —
// third-party/aws, third-party/codec/{hcl,protobuf},
// third-party/db/writer/{clickhouse,mysql,redis}, third-party/transform and
// third-party/x-crypto. Entitlement's ssh Identity is the framework's
// connector module framework/connectors/ssh (ADR 0158). What is left here is
// the opt-in yaml.v3 codec (third-party/codec/yaml, "yaml-full"). The
// Docker-backed integration suites live in the auxiliary e2e module
// (e2e/integration), outside go.work.
require (
	github.com/kitsunium/sdk/internal/core v0.1.16
	github.com/kitsunium/sdk/internal/kernel v0.1.16
	github.com/kitsunium/sdk/internal/service v0.1.16
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/kitsunium/sdk/internal/kernel => ./internal/kernel

replace github.com/kitsunium/sdk/internal/core => ./internal/core

replace github.com/kitsunium/sdk/internal/service => ./internal/service
