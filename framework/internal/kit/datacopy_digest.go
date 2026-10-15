package kit

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// The fingerprints a data copy is declared done by (ADR 0178 C7): never a
// count, which says nothing of a truncated document, a version written under
// the wrong number or a payload cut short. Every part of a fingerprint is
// prefixed by its length, so no two different sequences of parts hash the
// same bytes.

// digest is one SHA-256.
type digest = [sha256.Size]byte

// lengthPrefixed writes part into h behind its length, as eight big-endian
// bytes.
func lengthPrefixed(h hash.Hash, part []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(part)))
	h.Write(n[:])
	h.Write(part)
}

// digestOf is the SHA-256 of parts, each length-prefixed.
func digestOf(parts ...[]byte) digest {
	h := sha256.New()
	for _, p := range parts {
		lengthPrefixed(h, p)
	}
	var out digest
	h.Sum(out[:0])
	return out
}

// documentJSON is a document as both engines keep it, whatever the
// whitespace a file's snapshot wrote it with: compact, and HTML-escaped as
// json.Marshal writes one. Bytes that are not JSON are kept as they are, and
// differ from any document.
func documentJSON(raw []byte) []byte {
	var compact, escaped bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return raw
	}
	json.HTMLEscape(&escaped, compact.Bytes())
	return escaped.Bytes()
}

// documentDigest is a document's fingerprint: SHA-256(store ‖ key ‖ bytes),
// the document as the store keeps it — a sealed member a box, never opened.
func documentDigest(store, key string, raw []byte) digest {
	return digestOf([]byte("document"), []byte(store), []byte(key), documentJSON(raw))
}

// versionDigest is a version's fingerprint: SHA-256(store ‖ key ‖ number ‖
// instant ‖ metadata ‖ bytes), the metadata in sorted-key JSON — none is
// {} —, the instant to the nanosecond in UTC and empty when nobody recorded
// it: a renumbered or re-stamped version differs.
func versionDigest(store, key string, v *docstore.Version) (digest, error) {
	at := ""
	if !v.At.IsZero() {
		at = v.At.UTC().Format(time.RFC3339Nano)
	}
	meta := []byte("{}")
	if len(v.Meta) > 0 {
		var err error
		// encoding/json writes a map's keys sorted.
		if meta, err = json.Marshal(v.Meta); err != nil {
			return digest{}, err
		}
	}
	return digestOf([]byte("version"), []byte(store), []byte(key), []byte(strconv.FormatUint(v.Number, 10)),
		[]byte(at), meta, documentJSON(v.JSON)), nil
}

// trashDigest is the fingerprint of a document's place in the trash: the
// instant it was trashed.
func trashDigest(store, key string, at time.Time) digest {
	return digestOf([]byte("trash"), []byte(store), []byte(key), []byte(at.UTC().Format(time.RFC3339Nano)))
}

// messageDigest is a message's fingerprint: SHA-256(payload ‖ dead ‖
// deliveries ‖ reason ‖ code). Its identifier is not in it: the target mints
// its own.
func messageDigest(payload []byte, dead bool, deliveries int, reason string, code uint32) digest {
	state := "live"
	if dead {
		state = "dead"
	}
	return digestOf(payload, []byte(state), []byte(strconv.Itoa(deliveries)), []byte(reason),
		[]byte(strconv.FormatUint(uint64(code), 10)))
}
