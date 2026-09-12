package entitlement

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// roughtimeFixture is a complete, correctly signed Roughtime server: a pinned
// long-term key, a delegation it signed, and a batch containing one nonce.
//
// Everything is real ed25519 and real SHA-512. It proves the verifier is
// SELF-CONSISTENT — that it accepts what this encoder produces and refuses
// every mutation of it — and deliberately not that this dialect matches
// Cloudflare's or Netnod's, which no test in this repository can establish
// because UDP/2002 is unreachable from where it runs. That gap is stated in
// roughtime.go and is why RoughtimeServers ships empty.
type roughtimeFixture struct {
	// longTerm is the key a client pins.
	longTerm ed25519.PublicKey
	// longTermPriv signs the delegation.
	longTermPriv ed25519.PrivateKey
	// onlinePriv is the delegated key, which signs each response.
	onlinePriv ed25519.PrivateKey
	// onlinePub is its published half.
	onlinePub ed25519.PublicKey
}

// newRoughtimeFixture builds a server whose keys a test controls.
func newRoughtimeFixture(t *testing.T) *roughtimeFixture {
	t.Helper()

	longTerm, longTermPriv, longErr := ed25519.GenerateKey(nil)
	//: A failure here is an environment problem, not a test outcome.
	if longErr != nil {
		t.Fatalf("generating long-term key: %v", longErr)
	}
	onlinePub, onlinePriv, onlineErr := ed25519.GenerateKey(nil)
	//: A failure here is an environment problem, not a test outcome.
	if onlineErr != nil {
		t.Fatalf("generating online key: %v", onlineErr)
	}
	//: A server a test can speak for.
	return &roughtimeFixture{longTerm: longTerm, longTermPriv: longTermPriv, onlinePriv: onlinePriv, onlinePub: onlinePub}
}

// u32 renders a little-endian uint32 field.
func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// u64 renders a little-endian uint64 field.
func u64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

// certFor signs a delegation valid over the window given.
func (f *roughtimeFixture) certFor(notBefore, notAfter uint64) []byte {
	//: DELE < PUBK < MINT < MAXT is NOT the ascending order; encode sorts
	//: nothing, so the fields are listed in the order the format requires.
	dele := encodeRoughtimeMessage(sortFields([]roughtimeField{
		{tag: roughtimeTag("PUBK"), value: f.onlinePub},
		{tag: roughtimeTag("MINT"), value: u64(notBefore)},
		{tag: roughtimeTag("MAXT"), value: u64(notAfter)},
	}))
	sig := ed25519.Sign(f.longTermPriv, append([]byte(roughtimeDelegationContext), dele...))
	//: A certificate: the delegation, and the pinned key's signature on it.
	return encodeRoughtimeMessage(sortFields([]roughtimeField{
		{tag: roughtimeTag("SIG"), value: sig},
		{tag: roughtimeTag("DELE"), value: dele},
	}))
}

// roughtimeAnswer describes the answer a fixture should produce, including the
// ways it can be made wrong.
type roughtimeAnswer struct {
	// nonce is the challenge the batch commits to.
	nonce []byte
	// midpoint is the instant asserted.
	midpoint time.Time
	// signer signs the response. Nil means the delegated key, which is the
	// only one a client should accept.
	signer ed25519.PrivateKey
	// windowShift moves the delegation window away from the midpoint, so a
	// response signed outside the term it was delegated for can be built.
	windowShift time.Duration
}

// respond builds a complete, signed answer.
//
// The batch holds exactly one request, so the Merkle path is empty and the root
// IS the leaf. That is the ordinary shape for a lightly-loaded server and it
// exercises every signature; Test_verifyRoughtimeMerkle covers deeper trees,
// where the index actually decides something.
func (f *roughtimeFixture) respond(answer roughtimeAnswer) []byte {
	srep := encodeRoughtimeMessage(sortFields([]roughtimeField{
		{tag: roughtimeTag("RADI"), value: u32(uint32(time.Second / time.Microsecond))},
		{tag: roughtimeTag("MIDP"), value: u64(uint64(answer.midpoint.UnixMicro()))},
		{tag: roughtimeTag("ROOT"), value: hashRoughtimeLeaf(answer.nonce)},
	}))
	signer := f.onlinePriv
	//: A row that names its own signer is building an answer no delegation
	//: covers, which is the stolen-key case.
	if answer.signer != nil {
		signer = answer.signer
	}
	sig := ed25519.Sign(signer, append([]byte(roughtimeResponseContext), srep...))

	//: The window normally straddles the midpoint; a shift moves it clear of
	//: it entirely.
	centre := answer.midpoint.Add(answer.windowShift)
	cert := f.certFor(uint64(centre.Add(-time.Hour).UnixMicro()), uint64(centre.Add(time.Hour).UnixMicro()))

	message := encodeRoughtimeMessage(sortFields([]roughtimeField{
		{tag: roughtimeTag("SIG"), value: sig},
		{tag: roughtimeTag("PATH"), value: nil},
		{tag: roughtimeTag("SREP"), value: srep},
		{tag: roughtimeTag("CERT"), value: cert},
		{tag: roughtimeTag("INDX"), value: u32(0)},
	}))
	//: Framed exactly as the wire carries it.
	return append(append([]byte(roughtimeFrameMagic), u32(uint32(len(message)))...), message...)
}

// sortFields puts fields into the ascending tag order the format requires.
//
// encodeRoughtimeMessage deliberately does not sort — production builds one
// request and a wrong order there is a bug a test should catch, not something
// to paper over at runtime — so the fixture sorts on its behalf.
func sortFields(fields []roughtimeField) []roughtimeField {
	//: Insertion sort: five fields at most, and it keeps the helper readable.
	for i := 1; i < len(fields); i++ {
		for j := i; j > 0 && fields[j].tag < fields[j-1].tag; j-- {
			fields[j], fields[j-1] = fields[j-1], fields[j]
		}
	}
	//: Ascending as little-endian uint32, which is what the reader assumes.
	return fields
}

// Test_verifyRoughtimeResponse pins that every one of the four checks is
// load-bearing: drop any one and the answer becomes forgeable, replayable, or
// about somebody else's request.
func Test_verifyRoughtimeResponse(t *testing.T) {
	t.Parallel()

	midpoint := time.Now().Truncate(time.Microsecond)
	nonce := make([]byte, roughtimeNonceSize)

	tests := []struct {
		name string
		// mutate corrupts the fixture's answer, or the client's expectation.
		mutate  func(t *testing.T, f *roughtimeFixture, packet []byte, nonce []byte) (out []byte, key ed25519.PublicKey, checked []byte)
		wantErr bool
		reason  string
	}{
		{
			name: "a correctly signed answer verifies",
			mutate: func(_ *testing.T, f *roughtimeFixture, packet, nonce []byte) ([]byte, ed25519.PublicKey, []byte) {
				return packet, f.longTerm, nonce
			},
			reason: "the control: everything below differs from this in exactly one way",
		},
		{
			name: "a different long-term key is refused",
			mutate: func(t *testing.T, _ *roughtimeFixture, packet, nonce []byte) ([]byte, ed25519.PublicKey, []byte) {
				t.Helper()
				other, _, err := ed25519.GenerateKey(nil)
				//: A failure here is an environment problem, not a test outcome.
				if err != nil {
					t.Fatalf("generating impostor key: %v", err)
				}
				return packet, other, nonce
			},
			wantErr: true,
			reason:  "the pin is the only reason to believe the delegated key belongs to this server",
		},
		{
			name: "an answer to somebody else's nonce is refused",
			mutate: func(_ *testing.T, f *roughtimeFixture, packet, _ []byte) ([]byte, ed25519.PublicKey, []byte) {
				other := make([]byte, roughtimeNonceSize)
				other[0] = 0xAA
				return packet, f.longTerm, other
			},
			wantErr: true,
			reason:  "without the Merkle check this is a genuine signed answer, captured and replayed — which is exactly what the nonce exists to stop",
		},
		{
			//: NOT "flip the last byte": with a one-entry batch the path is
			//: empty, the index is never consulted, and corrupting it is
			//: genuinely inert. Flipping the first value instead lands in the
			//: response signature, which is covered.
			name: "a corrupted signature is refused",
			mutate: func(_ *testing.T, f *roughtimeFixture, packet, nonce []byte) ([]byte, ed25519.PublicKey, []byte) {
				corrupt := slices.Clone(packet)
				corrupt[len(corrupt)-1-roughtimeTagSize] ^= 0xFF
				return corrupt, f.longTerm, nonce
			},
			wantErr: true,
			reason:  "everything the signatures cover is covered; this row is where that is checked",
		},
		{
			//: The stolen-key case: a perfectly valid signature made with a
			//: key the pinned long-term half never delegated to.
			name: "a response signed by an undelegated key is refused",
			mutate: func(t *testing.T, f *roughtimeFixture, _, nonce []byte) ([]byte, ed25519.PublicKey, []byte) {
				t.Helper()
				_, rogue, err := ed25519.GenerateKey(nil)
				//: A failure here is an environment problem, not a test outcome.
				if err != nil {
					t.Fatalf("generating rogue key: %v", err)
				}
				return f.respond(roughtimeAnswer{nonce: nonce, midpoint: midpoint, signer: rogue}), f.longTerm, nonce
			},
			wantErr: true,
			reason:  "the delegation names which key may answer; any other one is a compromise or a mix-up",
		},
		{
			//: A response asserting an instant outside the term its delegation
			//: was valid for, which is what a delegated key kept past its
			//: expiry produces.
			name: "a midpoint outside the delegation window is refused",
			mutate: func(_ *testing.T, f *roughtimeFixture, _, nonce []byte) ([]byte, ed25519.PublicKey, []byte) {
				return f.respond(roughtimeAnswer{nonce: nonce, midpoint: midpoint, windowShift: 48 * time.Hour}), f.longTerm, nonce
			},
			wantErr: true,
			reason:  "the window is what bounds the damage a leaked online key can do",
		},
		{
			name: "an unframed packet is refused",
			mutate: func(_ *testing.T, f *roughtimeFixture, packet, nonce []byte) ([]byte, ed25519.PublicKey, []byte) {
				return packet[len(roughtimeFrameMagic)+roughtimeTagSize:], f.longTerm, nonce
			},
			wantErr: true,
			reason:  "the frame is how a reader knows where the message ends",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := newRoughtimeFixture(t)
			built := fixture.respond(roughtimeAnswer{nonce: nonce, midpoint: midpoint})
			packet, key, checked := tt.mutate(t, fixture, built, nonce)

			got, radius, err := verifyRoughtimeResponse(packet, checked, key)
			if tt.wantErr {
				//: Every refusal here is "could not verify", never a denial.
				if !errors.Is(err, coreent.ErrCIUnverifiable) {
					t.Errorf("verifyRoughtimeResponse() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifyRoughtimeResponse() error = %v, want nil (%s)", err, tt.reason)
			}
			if !got.Equal(midpoint) {
				t.Errorf("verifyRoughtimeResponse() midpoint = %s, want %s (%s)", got, midpoint, tt.reason)
			}
			if radius != time.Second {
				t.Errorf("verifyRoughtimeResponse() radius = %s, want 1s (%s)", radius, tt.reason)
			}
		})
	}
}

// Test_verifyRoughtimeMerkle pins the proof on trees deeper than one, where the
// index actually decides something.
//
// A single-entry batch verifies with an empty path and would pass even if the
// left/right decision were inverted. These rows are where that bug would show.
func Test_verifyRoughtimeMerkle(t *testing.T) {
	t.Parallel()

	nonce := []byte("a nonce of exactly thirty-two by")

	tests := []struct {
		name string
		// index is our position in the batch.
		index uint32
		// corrupt breaks the proof after it is built.
		corrupt bool
		wantErr bool
		reason  string
	}{
		{name: "a left leaf reaches the root", index: 0, reason: "index bit clear means our hash is the LEFT child"},
		{name: "a right leaf reaches the root", index: 1, reason: "index bit set means our hash is the RIGHT child — inverting this is the bug a one-entry batch cannot catch"},
		{name: "a tampered sibling does not", index: 1, corrupt: true, wantErr: true, reason: "the root commits to every node on the way up"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sibling := make([]byte, roughtimeHashSize)
			sibling[0] = 0x42
			leaf := hashRoughtimeLeaf(nonce)

			//: Build the root the way the server would, given our side.
			root := hashRoughtimeNode(leaf, sibling)
			if tt.index&1 == 1 {
				root = hashRoughtimeNode(sibling, leaf)
			}
			//: The corrupt row hands over a sibling that was not used.
			path := slices.Clone(sibling)
			if tt.corrupt {
				path[0] ^= 0xFF
			}

			fields := map[uint32][]byte{
				roughtimeTag("INDX"): u32(tt.index),
				roughtimeTag("PATH"): path,
			}
			err := verifyRoughtimeMerkle(fields, nonce, root)
			if tt.wantErr {
				if !errors.Is(err, coreent.ErrCIUnverifiable) {
					t.Errorf("verifyRoughtimeMerkle() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Errorf("verifyRoughtimeMerkle() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}
