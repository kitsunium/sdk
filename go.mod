module github.com/kitsunium/sdk

go 1.27.1

// The root module is the workspace's anchor: nothing requires it, the release
// chain leaves it out so it is never tagged, and it holds no package. Every
// vendor integration under third-party/ is a module of its own, in that chain
// (ADR 0157) — third-party/aws, third-party/codec/{hcl,protobuf,yaml},
// third-party/db/writer/{clickhouse,mysql,redis}, third-party/transform and
// third-party/x-crypto. Entitlement's ssh Identity, the last package it held,
// is the framework's connector module framework/connectors/ssh (ADR 0158). The
// Docker-backed integration suites live in the auxiliary e2e module
// (e2e/integration), outside go.work.
