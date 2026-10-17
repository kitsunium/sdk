package kit

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// A store's versions as its engines keep them (revisions_engine.go, ADR
// 0007 §3): each engine stamps a write with what its caller says and hands
// its versions on as they rest — plug.VersionKeeper —, the engines that wrap
// one hand them on too.

// pluggedVersions are the SDK's versions as the store's engine hands them on.
func pluggedVersions(vs []docstore.Version) []plug.VersionValue {
	if vs == nil {
		return nil
	}
	out := make([]plug.VersionValue, len(vs))
	for i, v := range vs {
		out[i] = plug.VersionValue{Number: v.Number, At: v.At, Meta: v.Meta, JSON: v.JSON}
	}
	return out
}

// sdkVersions are the store engine's versions as the SDK takes them back.
func sdkVersions(vs []plug.VersionValue) []docstore.Version {
	if vs == nil {
		return nil
	}
	out := make([]docstore.Version, len(vs))
	for i, v := range vs {
		out[i] = docstore.Version{Number: v.Number, At: v.At, Meta: v.Meta, JSON: v.JSON}
	}
	return out
}

// rewritingSDK is fn, a rewrite of the store engine's versions, as the
// SDK's document store applies it.
func rewritingSDK(fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) func([]docstore.Version) ([]docstore.Version, error) {
	return func(vs []docstore.Version) ([]docstore.Version, error) {
		out, err := fn(pluggedVersions(vs))
		if err != nil {
			return nil, err
		}
		return sdkVersions(out), nil
	}
}

// The document store, in memory and in the data directory ---------------------

// stamp is what a write under ctx says about the version it makes; nothing
// on a store that keeps no versions.
func (e *docEngine) stamp(ctx context.Context) docstore.Stamp {
	if e.keep == 0 {
		return docstore.Stamp{}
	}
	return stampOf(ctx)
}

// StoredVersions are the versions of the record under key as they rest.
//
//ktn:allow-unused-param: the VersionKeeper interface passes a context the local document store does not take
func (e *docEngine) StoredVersions(_ context.Context, key string) ([]plug.VersionValue, error) {
	vs, err := e.ds.Versions(key)
	return pluggedVersions(vs), err
}

// RewriteVersions rewrites the former versions of the record under key
// with fn, under the writers' lock.
//
//ktn:allow-unused-param: the VersionKeeper interface passes a context the local document store does not take
func (e *docEngine) RewriteVersions(_ context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	return e.ds.RewriteVersions(key, rewritingSDK(fn))
}

// head is the number of the current version of the record under key,
// before a write of a transaction replaces it: a rollback takes every
// version above it out of the history. Zero on a store that keeps no
// versions.
func (e *docEngine) head(key string) uint64 {
	if e.keep == 0 {
		return 0
	}
	all, err := e.ds.Versions(key)
	if err != nil || len(all) == 0 {
		return 0
	}
	return all[0].Number
}

// forget takes out of the history of the record under key the versions a
// rolled back write made — every former version numbered above head, the
// version the write found — and gives head back the document it held, prev:
// a write in place, a workflow's transition of the state alone, changed it
// without a new number. What never committed never shows. The version the
// rollback made stays: a number is never given twice.
func (e *docEngine) forget(key string, head uint64, prev docRecord) error {
	if e.keep == 0 || head == 0 {
		return nil
	}
	held, merr := json.Marshal(prev)
	err := e.ds.RewriteVersions(key, func(former []docstore.Version) ([]docstore.Version, error) {
		former = slices.DeleteFunc(former, func(v docstore.Version) bool { return v.Number > head })
		for i := range former {
			if former[i].Number == head && merr == nil {
				former[i].JSON = held
			}
		}
		return former, nil
	})
	if errors.Is(err, docstore.WriteUnconfirmed) {
		return nil
	}
	return err
}

// The document store over SQL -------------------------------------------------

// stamp is what a write under ctx — the caller's, never the transaction's
// own, which carries the outermost caller's — says about the version it
// makes; nothing on a store that keeps no versions.
func (e *sqlEngine) stamp(ctx context.Context) docstore.Stamp {
	if e.keep == 0 {
		return docstore.Stamp{}
	}
	return stampOf(ctx)
}

// StoredVersions are the versions of the record under key as the table
// keeps them.
func (e *sqlEngine) StoredVersions(ctx context.Context, key string) ([]plug.VersionValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	vs, err := e.ds.Versions(c, key)
	return pluggedVersions(vs), err
}

// RewriteVersions rewrites the former versions of the record under key
// with fn, in one write.
func (e *sqlEngine) RewriteVersions(ctx context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	return e.ds.RewriteVersions(c, key, rewritingSDK(fn))
}

// The engines that wrap one ---------------------------------------------------

// keptVersions are the versions eng keeps, or VersionsNotKept for an engine
// that keeps none.
func keptVersions(ctx context.Context, eng plug.StoreEngine, key string) ([]plug.VersionValue, error) {
	vk, ok := eng.(plug.VersionKeeper)
	if !ok {
		return nil, docstore.VersionsNotKept
	}
	return vk.StoredVersions(ctx, key)
}

// rewriteKept rewrites the versions eng keeps, or is VersionsNotKept for an
// engine that keeps none.
func rewriteKept(ctx context.Context, eng plug.StoreEngine, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	vk, ok := eng.(plug.VersionKeeper)
	if !ok {
		return docstore.VersionsNotKept
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
