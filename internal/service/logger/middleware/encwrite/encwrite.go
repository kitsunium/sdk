// Package encwrite — the EncWriter decorator that seals each record's bytes
// under a per-sink subkey and frames the box for a downstream byte sink.
package encwrite

import (
	"context"
	"encoding/binary"
	"math"
	"sync"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// aeadAlgorithm is the registry key of the default AES-256-GCM scheme used
	// to seal each record's bytes (activated by the aesgcm blank import).
	aeadAlgorithm corecrypto.Algorithm = "aes-256-gcm"
	// kdfAlgorithm is the registry key of the HKDF-SHA256 deriver used to
	// derive a per-sink subkey (activated by the hkdfsha256 blank import).
	kdfAlgorithm corecrypto.Algorithm = "hkdf-sha256"
	// lenPrefixBytes is the width of the big-endian sealed-box length prefix.
	lenPrefixBytes int = 4
	// defaultInfo is the HKDF info label applied when Config.Info is empty; it
	// binds the derived subkey to the encrypting-sink purpose.
	defaultInfo string = "encwrite-sink"
)

// EncWriter seals each record's bytes under a per-sink subkey and writes the
// length-prefixed sealed box to the downstream sink. Safe for concurrent use.
type EncWriter struct {
	// mu serializes Write / Flush / Close against concurrent producers.
	mu sync.Mutex
	// downstream receives the framed sealed box plus the originating record.
	downstream corelogger.Sink
	// key is the master key, retained so Close can zeroize it on every path.
	key corecrypto.Key
	// subkey is the derived per-sink key used for sealing; zeroized on Close.
	subkey corecrypto.Key
}

// NewEncWriter builds an EncWriter from cfg, deriving the per-sink subkey
// via HKDF-SHA256. It returns an error wrapping EncWriteSealFailed when the
// subkey cannot be derived from cfg.Key.
func NewEncWriter(cfg Config) (writer *EncWriter, err error) {
	//: pick the configured info label or fall back to the purpose default.
	info := cfg.Info
	//: an empty label would leave the subkey unbound to a purpose.
	if info == "" {
		//: substitute the package default purpose label.
		info = defaultInfo
	}
	//: derive a per-sink subkey so the raw master key never seals directly.
	raw, derr := corecrypto.Subkey(kdfAlgorithm, cfg.Key.Bytes(), nil, info, corecrypto.KeyLen)
	//: a derivation fault (unknown KDF / over-long length) aborts construction.
	if derr != nil {
		//: surface the typed sentinel; consumers HasCode(err, CodeEncWriteSealFailed).
		return nil, errs.Wrap(derr, errs.WrapParams{
			Code:    CodeEncWriteSealFailed,
			Reason:  "ENC_WRITE_SEAL_FAILED",
			Public:  "Encrypting middleware could not seal the record",
			Private: "service/logger/middleware/encwrite: Subkey derivation failed at construction",
		})
	}
	//: wrap the raw subkey so it is sealable and zeroizable as a Key value.
	subkey, kerr := corecrypto.NewKey(raw)
	//: an unexpected length from Subkey is a defensive guard, never reached.
	if kerr != nil {
		//: same typed sentinel — the subkey could not be materialised.
		return nil, errs.Wrap(kerr, errs.WrapParams{
			Code:    CodeEncWriteSealFailed,
			Reason:  "ENC_WRITE_SEAL_FAILED",
			Public:  "Encrypting middleware could not seal the record",
			Private: "service/logger/middleware/encwrite: derived subkey was not KeyLen bytes",
		})
	}
	//: hand back the ready sink wrapping the configured downstream.
	return &EncWriter{downstream: cfg.Sink, key: cfg.Key, subkey: subkey}, nil
}

// Write seals p under the per-sink subkey, frames it with a length prefix, and
// delivers it to the downstream sink. It returns the downstream byte count.
func (s *EncWriter) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: seal the record bytes under the derived subkey (nonce hidden in the box).
	box, serr := corecrypto.Seal(aeadAlgorithm, s.subkey, p, nil)
	//: a seal fault (entropy / unknown scheme) aborts before delivery.
	if serr != nil {
		//: wrap the cause under the typed seal sentinel.
		return 0, errs.Wrap(serr, errs.WrapParams{
			Code:    CodeEncWriteSealFailed,
			Reason:  "ENC_WRITE_SEAL_FAILED",
			Public:  "Encrypting middleware could not seal the record",
			Private: "service/logger/middleware/encwrite: Seal returned an error",
		})
	}
	//: length-prefix the sealed box so byte sinks can recover record boundaries.
	framed, ferr := frame(box)
	//: a box too large for the 4-byte prefix aborts before delivery.
	if ferr != nil {
		//: propagate the framing sentinel unchanged.
		return 0, ferr
	}
	//: hand the framed box plus the record to the downstream sink.
	return s.downstream.Write(ctx, r, framed)
}

// Flush forwards the flush to the downstream sink; this middleware buffers
// nothing of its own.
func (s *EncWriter) Flush(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: delegate straight to the downstream sink.
	return s.downstream.Flush(ctx)
}

// Close zeroizes the master key and the derived subkey on every path, then
// closes the downstream sink.
func (s *EncWriter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: clear the master key material before returning, regardless of outcome.
	s.key.Zeroize()
	//: clear the derived subkey too so no copy of the secret survives.
	s.subkey.Zeroize()
	//: release the downstream sink's resources.
	return s.downstream.Close()
}

// frame prepends a 4-byte big-endian length prefix to box, returning
// [len(box) as uint32 big-endian || box]. A box too large for a uint32 length
// returns FramingFailed.
func frame(box []byte) (framed []byte, err error) {
	//: reject boxes too large for the 32-bit length prefix.
	if len(box) > math.MaxUint32 {
		//: no underlying cause — return the framing sentinel directly.
		return nil, FramingFailed
	}
	//: allocate exactly the prefix plus the payload, no slack.
	framed = make([]byte, lenPrefixBytes+len(box))
	//: write the box length into the leading 4 bytes, big-endian.
	binary.BigEndian.PutUint32(framed[:lenPrefixBytes], uint32(len(box)))
	//: copy the sealed box after the prefix.
	copy(framed[lenPrefixBytes:], box)
	//: framed sealed box ready for the downstream sink.
	return framed, nil
}
