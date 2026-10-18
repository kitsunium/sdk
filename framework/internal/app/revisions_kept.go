package app

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
)

// A store's versions as its engines keep them (revisions_engine.go, ADR
// 0007 §3): the store subsystem's engines stamp a write with what its caller
// says (stampOf) and hand their versions on as they rest — plug.VersionKeeper
// —, the engines that wrap one hand them on too.

// The engines that wrap one ---------------------------------------------------

// keptVersions are the versions eng keeps, or VersionsNotKept for an engine
// that keeps none.
func keptVersions(ctx context.Context, eng plug.StoreEngine, key string) ([]plug.VersionValue, error) {
	vk, ok := eng.(plug.VersionKeeper)
	if !ok {
		return nil, errVersionsNotKept
	}
	return vk.StoredVersions(ctx, key)
}

// rewriteKept rewrites the versions eng keeps, or is VersionsNotKept for an
// engine that keeps none.
func rewriteKept(ctx context.Context, eng plug.StoreEngine, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	vk, ok := eng.(plug.VersionKeeper)
	if !ok {
		return errVersionsNotKept
	}
	return vk.RewriteVersions(ctx, key, fn)
}

// StoredVersions are the versions the engine a history wraps keeps.
func (h *historied) StoredVersions(ctx context.Context, key string) ([]plug.VersionValue, error) {
	return keptVersions(ctx, h.StoreEngine, key)
}

// RewriteVersions rewrites the versions the engine a history wraps keeps.
func (h *historied) RewriteVersions(ctx context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	return rewriteKept(ctx, h.StoreEngine, key, fn)
}

// StoredVersions are a sealing store's versions as they rest: its sealed
// members boxes, bound where the record's are.
func (e *sealedEngine) StoredVersions(ctx context.Context, key string) ([]plug.VersionValue, error) {
	return keptVersions(ctx, e.inner, key)
}

// RewriteVersions rewrites a sealing store's versions as they rest.
func (e *sealedEngine) RewriteVersions(ctx context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	return rewriteKept(ctx, e.inner, key, fn)
}
