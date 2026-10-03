module github.com/kitsunium/sdk

go 1.27.1

// The SDK is this one module (ADR 0162): the kernel, core and service layers
// under internal/, the public packages under pkg/v1/ and the framework under
// framework/. Import paths are github.com/kitsunium/sdk/pkg/v1/... and
// github.com/kitsunium/sdk/framework/..., and Go's internal/ rule keeps
// consumers out of internal/. It requires no module outside the standard
// library (ADR 0156), and a release is one tag on it, vX.Y.Z.
//
// The modules that require a vendor stay modules of their own, so a consumer of
// one integration takes that vendor's graph and no other: the nine under
// third-party/ (ADR 0157) and the four framework connectors under
// framework/connectors/ (ADR 0147, ADR 0158). Each requires this module and is
// tagged <dir>/vX.Y.Z only by a release that changes it. The Docker-backed
// integration suites live in the auxiliary e2e module, outside go.work with
// tools/genindex and tools/sdkguard.
