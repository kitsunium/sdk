package kit

import (
	"context"
	"crypto/sha256"
	"errors"
	"iter"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A store's fingerprint, and the walk that names where two differ (ADR 0178
// C7): the running SHA-256 of a table over its records in key order, each
// record's fingerprint made of its document, its versions and its place in
// the trash (datacopy_digest.go).

// keyedDigest is one record's fingerprint, and its key.
type keyedDigest struct {
	key string
	sum digest
}

// recordsInOrder yields every record of docs in key order — the keys it
// holds and those in its trash, merged —, each with the instant it was
// trashed, zero for a document it holds.
func recordsInOrder(ctx context.Context, docs copyDocs) iter.Seq2[plug.TombstoneValue, error] {
	return func(yield func(plug.TombstoneValue, error) bool) {
		tombs, err := docs.TombstoneList(ctx)
		if err != nil {
			yield(plug.TombstoneValue{}, err)
			return
		}
		for key, err := range docs.KeyList(ctx) {
			if err != nil {
				yield(plug.TombstoneValue{}, err)
				return
			}
			var ok bool
			if tombs, ok = yieldTrashedBefore(yield, tombs, key); !ok || !yield(plug.TombstoneValue{Key: key}, nil) {
				return
			}
		}
		for _, t := range tombs {
			if !yield(t, nil) {
				return
			}
		}
	}
}

// yieldTrashedBefore yields the trashed records whose key sorts before key,
// and returns those left and whether the caller wants more.
func yieldTrashedBefore(yield func(plug.TombstoneValue, error) bool, tombs []plug.TombstoneValue, key string) ([]plug.TombstoneValue, bool) {
	for len(tombs) > 0 && tombs[0].Key < key {
		if !yield(tombs[0], nil) {
			return tombs, false
		}
		tombs = tombs[1:]
	}
	return tombs, true
}

// recordDigests yields every record of docs in key order with its
// fingerprint.
func recordDigests(ctx context.Context, table string, docs copyDocs) iter.Seq2[keyedDigest, error] {
	return func(yield func(keyedDigest, error) bool) {
		for rec, err := range recordsInOrder(ctx, docs) {
			if err != nil {
				yield(keyedDigest{}, err)
				return
			}
			sum, err := recordDigest(ctx, table, docs, rec.Key, rec.At)
			if !yield(keyedDigest{key: rec.Key, sum: sum}, err) || err != nil {
				return
			}
		}
	}
}

// recordDigest is the fingerprint of the record key of docs: its document
// unless it is in the trash, each of its versions when the store keeps them,
// and the instant it was trashed.
func recordDigest(ctx context.Context, table string, docs copyDocs, key string, trashedAt time.Time) (digest, error) {
	h := sha256.New()
	if trashedAt.IsZero() {
		raw, err := docs.Current(ctx, key)
		if err != nil {
			return digest{}, err
		}
		sum := documentDigest(table, key, raw)
		h.Write(sum[:])
	} else {
		sum := trashDigest(table, key, trashedAt)
		h.Write(sum[:])
	}
	if docs.KeepsVersions() {
		versions, err := docs.VersionList(ctx, key)
		if err != nil {
			return digest{}, err
		}
		for i := range versions {
			sum, err := versionDigest(table, key, &versions[i])
			if err != nil {
				return digest{}, err
			}
			h.Write(sum[:])
		}
	}
	var out digest
	h.Sum(out[:0])
	return out, nil
}

// storeDigest is a table's fingerprint: a running SHA-256 over its records'
// fingerprints, in key order, each behind its key.
func storeDigest(ctx context.Context, table string, docs copyDocs) (digest, error) {
	h := sha256.New()
	for rec, err := range recordDigests(ctx, table, docs) {
		if err != nil {
			return digest{}, err
		}
		lengthPrefixed(h, []byte(rec.key))
		h.Write(rec.sum[:])
	}
	var out digest
	h.Sum(out[:0])
	return out, nil
}

// verifyDocuments compares the fingerprints of both sides of p: equal, the
// table is copied; different, both sides are walked again to name the first
// key that differs — one side's only, or with another fingerprint.
func verifyDocuments(ctx context.Context, p copyPart) error {
	src, err := storeDigest(ctx, p.name, p.from)
	if err != nil {
		return err
	}
	dst, err := storeDigest(ctx, p.name, p.to)
	if err != nil {
		return err
	}
	if src == dst {
		return nil
	}
	key, err := firstDivergence(ctx, p)
	if err != nil {
		return err
	}
	return failure(CodeDataCopyDiverged, "DATA_COPY_DIVERGED", "a store differs once copied: the copy is rolled back", nil,
		errs.String("table", p.name), errs.String("key", key))
}

// firstDivergence walks both sides of p in key order and names the first
// key whose record differs, or that one side holds and the other does not.
func firstDivergence(ctx context.Context, p copyPart) (string, error) {
	nextSrc, stopSrc := iter.Pull2(recordDigests(ctx, p.name, p.from))
	defer stopSrc()
	nextDst, stopDst := iter.Pull2(recordDigests(ctx, p.name, p.to))
	defer stopDst()
	for {
		s, sErr, sOK := nextSrc()
		d, dErr, dOK := nextDst()
		if err := errors.Join(sErr, dErr); err != nil {
			return "", err
		}
		if key, differs := divergence(s, sOK, d, dOK); differs || (!sOK && !dOK) {
			return key, nil
		}
	}
}

// divergence compares the next record of each side: the key that differs,
// and whether one does — a record one side holds alone, or two fingerprints
// of one key.
func divergence(s keyedDigest, sOK bool, d keyedDigest, dOK bool) (string, bool) {
	switch {
	case !sOK && !dOK:
		return "", false
	case !sOK:
		return d.key, true
	case !dOK, s.key == d.key && s.sum != d.sum:
		return s.key, true
	case s.key != d.key:
		return min(s.key, d.key), true
	default:
		return "", false
	}
}
