package encwrite

import (
	"context"
	"encoding/binary"
	"math"
	"sync"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	coreencwrite "github.com/kitsunium/sdk/internal/core/observe/logger/middleware/encwrite"
	"github.com/kitsunium/sdk/internal/kernel/concur/buffer"
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
	// sealOverhead is what AES-256-GCM's box adds to a record: the version and
	// id bytes, the 12-byte nonce and the 16-byte tag. It sizes the borrowed
	// frame so the seal never grows it.
	sealOverhead int = 2 + 12 + 16
)

// keyBytes is the seam through which deriveSubkey obtains a fresh copy of the
// master key material; it is a package var solely so a white-box test can wrap
// it and assert the returned copy is zeroized before deriveSubkey returns
// (finding V29). Production always binds it to corecrypto.Key.Bytes.
var keyBytes func(k corecrypto.Key) []byte = func(k corecrypto.Key) []byte {
	//: hand back the cipher-ready master-key copy from the opaque Key value.
	return k.Bytes()
}

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
	// sealer is the AEAD bound to subkey once, at construction (#813): the
	// cipher is not rebuilt per record. Nil when the scheme was not
	// registered then, and after Close, which drops the cipher with the key:
	// a record is then sealed by crypto.Seal under subkey as it stands.
	sealer corecrypto.Sealer
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
	//: derive the per-sink subkey, zeroizing every intermediate secret copy.
	subkey, derr := deriveSubkey(cfg.Key, info)
	//: a derivation or materialisation fault aborts construction.
	if derr != nil {
		//: propagate the already-typed seal sentinel from the helper unchanged.
		return nil, derr
	}
	//: bind the subkey once, so no record rebuilds the cipher.
	sealer, bindErr := corecrypto.NewSealer(aeadAlgorithm, subkey)
	//: an AEAD not registered yet: the per-record Seal reports it at the first
	//: Write, as it always has.
	if bindErr != nil {
		sealer = nil
	}
	//: hand back the ready sink wrapping the configured downstream.
	return &EncWriter{downstream: cfg.Sink, key: cfg.Key, subkey: subkey, sealer: sealer}, nil
}

// deriveSubkey derives the per-sink subkey from master via HKDF-SHA256 under the
// info label and returns it as a zeroizable Key. It zeroizes both intermediate
// secret copies — the master-key clone and the raw derived bytes — before
// returning so no un-zeroized key material lingers on the heap (finding V29).
func deriveSubkey(master corecrypto.Key, info string) (subkey corecrypto.Key, err error) {
	//: take a single cipher-ready copy of the master key through the seam.
	secret := keyBytes(master)
	//: wipe that master-key copy on every return path — it is never retained.
	defer clear(secret)
	//: derive a per-sink subkey so the raw master key never seals directly.
	raw, derr := corecrypto.Subkey(kdfAlgorithm, secret, nil, info, corecrypto.KeyLen)
	//: a derivation fault (unknown KDF / over-long length) aborts construction.
	if derr != nil {
		//: surface the typed sentinel; consumers HasCode(err, CodeEncWriteSealFailed).
		return corecrypto.Key{}, errs.Wrap(derr, errs.WrapParams{
			Code:    coreencwrite.CodeEncWriteSealFailed,
			Reason:  "ENC_WRITE_SEAL_FAILED",
			Public:  "Encrypting middleware could not seal the record",
			Private: "service/observe/logger/middleware/encwrite: Subkey derivation failed at construction",
		})
	}
	//: NewKey clones raw, so wipe our copy of the live subkey on every return.
	defer clear(raw)
	//: wrap the raw subkey so it is sealable and zeroizable as a Key value.
	key, kerr := corecrypto.NewKey(raw)
	//: an unexpected length from Subkey is a defensive guard, never reached.
	if kerr != nil {
		//: same typed sentinel — the subkey could not be materialised.
		return corecrypto.Key{}, errs.Wrap(kerr, errs.WrapParams{
			Code:    coreencwrite.CodeEncWriteSealFailed,
			Reason:  "ENC_WRITE_SEAL_FAILED",
			Public:  "Encrypting middleware could not seal the record",
			Private: "service/observe/logger/middleware/encwrite: derived subkey was not KeyLen bytes",
		})
	}
	//: ready zeroizable subkey; both intermediate copies are wiped on return.
	return key, nil
}

// Write seals p under the per-sink subkey, frames it with a length prefix, and
// delivers it to the downstream sink. It returns the downstream byte count.
func (s *EncWriter) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bp := buffer.GetSize(lenPrefixBytes + len(p) + sealOverhead)
	defer buffer.Put(bp)
	framed, ferr := s.sealFramed((*bp)[:0], p)
	*bp = framed
	//: a seal or framing fault aborts before delivery.
	if ferr != nil {
		//: the typed EncWriteSealFailed or FramingFailed.
		return 0, ferr
	}
	//: hand the framed box plus the record to the downstream sink.
	return s.downstream.Write(ctx, r, framed)
}

// WantsAttrs answers corelogger.AttrsRequester for the downstream sink,
// which receives the record unchanged.
func (s *EncWriter) WantsAttrs() bool {
	//: the record reaches the wrapped sink as it came.
	return corelogger.SinkWantsAttrs(s.downstream)
}

// sealFramed appends to dst the 4-byte length prefix and the box of p sealed
// under the per-sink subkey (nonce hidden in the box), then writes the box's
// length into the prefix.
func (s *EncWriter) sealFramed(dst, p []byte) (framed []byte, err error) {
	framed, serr := s.sealTo(append(dst, 0, 0, 0, 0), p)
	//: a seal fault (entropy / unknown scheme) aborts before delivery.
	if serr != nil {
		//: wrap the cause under the typed seal sentinel.
		return framed, errs.Wrap(serr, errs.WrapParams{
			Code:    coreencwrite.CodeEncWriteSealFailed,
			Reason:  "ENC_WRITE_SEAL_FAILED",
			Public:  "Encrypting middleware could not seal the record",
			Private: "service/observe/logger/middleware/encwrite: Seal returned an error",
		})
	}
	//: the box's length, big-endian, in the prefix reserved before it.
	return framed, stampLength(framed)
}

// sealTo appends the box of p to dst: through the sealer bound at
// construction, or crypto.Seal when there is none.
func (s *EncWriter) sealTo(dst, p []byte) (sealed []byte, err error) {
	//: the cipher built once.
	if s.sealer != nil {
		//: the box after dst's prefix.
		return s.sealer.SealTo(dst, p, nil)
	}
	box, serr := corecrypto.Seal(aeadAlgorithm, s.subkey, p, nil)
	//: the scheme is missing or the host's entropy failed.
	if serr != nil {
		//: dst unextended.
		return dst, serr
	}
	//: the box after dst's prefix.
	return append(dst, box...), nil
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
	//: and drop the cipher built from it: its round keys go with it.
	s.sealer = nil
	//: release the downstream sink's resources.
	return s.downstream.Close()
}

// stampLength writes the length of the box that follows framed's 4-byte
// prefix into that prefix, big-endian. A box too large for a uint32 length
// returns FramingFailed.
func stampLength(framed []byte) error {
	boxLen := uint64(len(framed) - lenPrefixBytes)
	//: reject boxes too large for the 32-bit length prefix; widen to uint64 first
	//: so the MaxUint32 constant does not overflow int on a 32-bit GOARCH.
	if boxLen > math.MaxUint32 {
		//: no underlying cause — return the framing sentinel directly.
		return coreencwrite.FramingFailed
	}
	//: write the box length into the leading 4 bytes, big-endian.
	binary.BigEndian.PutUint32(framed[:lenPrefixBytes], uint32(boxLen))
	//: the frame is complete.
	return nil
}
