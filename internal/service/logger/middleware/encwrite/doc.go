// Package encwrite encrypts each log record's bytes before delivery to a
// downstream sink — the transform-the-bytes middleware category (ADR 0014 D5).
//
// EncWriter wraps a core/logger.Sink. At construction it derives a per-sink
// subkey from the configured master key via HKDF-SHA256 (crypto.Subkey), so the
// raw key never seals directly. For every record it seals the record bytes under
// that subkey with AES-256-GCM (crypto.Seal, nonce hidden in the box), then
// frames the sealed box with a 4-byte big-endian length prefix —
// [uint32 len(box)][box] — before handing it to the downstream sink. Close
// zeroizes both the master key and the derived subkey on every path.
//
// Activate the schemes by blank-importing the AEAD and deriver packages:
//
//	import (
//		_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
//		_ "github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
//	)
package encwrite
