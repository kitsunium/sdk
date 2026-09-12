// Package entitlement - verifying a Roughtime answer.
//
// Three signatures and a proof, in an order chosen so nothing downstream runs
// on bytes upstream has not vouched for: the long-term key signs a delegation,
// the delegated key signs the response, the response commits to a Merkle root,
// and the root is shown to contain the nonce this process drew a moment ago.
// Drop any one of the four and the answer becomes replayable, forgeable, or
// about somebody else's request.
package entitlement

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"fmt"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// Merkle node prefixes. They exist so a leaf hash can never be mistaken for an
// interior one, which is what stops a second-preimage attack on the tree.
const (
	// roughtimeLeafPrefix marks a hash over a client nonce.
	roughtimeLeafPrefix byte = 0x00
	// roughtimeNodePrefix marks a hash over two child hashes.
	roughtimeNodePrefix byte = 0x01
)

// verifyRoughtimeResponse authenticates a server's answer and returns the
// instant it attests to.
//
// Order is the whole design: the delegation is checked against the pinned
// long-term key first, so the key that signs the response is one the server's
// owner actually delegated to; then the response; then the tree, which is what
// ties any of it to THIS request rather than to a capture of somebody else's.
func verifyRoughtimeResponse(packet, nonce []byte, longTerm ed25519.PublicKey) (midpoint time.Time, radius time.Duration, err error) {
	message, frameErr := unframeRoughtime(packet)
	//: A packet that is not framed as one is not an answer.
	if frameErr != nil {
		//: Propagate the malformed answer.
		return time.Time{}, 0, frameErr
	}
	fields, decodeErr := decodeRoughtimeMessage(message)
	//: A message we cannot take apart carries nothing.
	if decodeErr != nil {
		//: Propagate the malformed answer.
		return time.Time{}, 0, decodeErr
	}

	delegated, deleErr := verifyRoughtimeCert(fields[roughtimeTag("CERT")], longTerm)
	//: A delegation the server's own key did not sign authorises no answer.
	if deleErr != nil {
		//: Propagate the refusal.
		return time.Time{}, 0, deleErr
	}

	srep := fields[roughtimeTag("SREP")]
	//: The delegated key must have signed this exact response.
	if sigErr := checkRoughtimeSignature(delegated.key, roughtimeResponseContext, srep, fields[roughtimeTag("SIG")]); sigErr != nil {
		//: Propagate the refusal.
		return time.Time{}, 0, fmt.Errorf("roughtime response: %w", sigErr)
	}

	stated, statedErr := readRoughtimeSrep(srep)
	//: A signed response we cannot read is not one.
	if statedErr != nil {
		//: Propagate the malformed answer.
		return time.Time{}, 0, statedErr
	}
	//: What the response asserts, checked against what it was allowed to
	//: assert and against what THIS client asked.
	if boundErr := checkRoughtimeBinding(stated, delegated, fields, nonce); boundErr != nil {
		//: Propagate the refusal.
		return time.Time{}, 0, boundErr
	}
	//: Microseconds since the epoch, with the server's own uncertainty.
	return time.UnixMicro(int64(stated.midpoint)).UTC(), time.Duration(stated.radius) * time.Microsecond, nil
}

// checkRoughtimeBinding ties a signed statement to the delegation that allowed
// it and to the request that asked for it.
//
// Two checks, both indispensable and both easy to leave out because the
// signatures already passed. Without the WINDOW, a delegated key kept past its
// term keeps answering forever. Without the TREE, the answer is a genuine,
// correctly signed statement about somebody else's request, captured and
// replayed — which is exactly what the nonce exists to prevent.
func checkRoughtimeBinding(stated roughtimeStatement, delegated roughtimeDelegation, fields map[uint32][]byte, nonce []byte) error {
	//: A response signed outside the window its delegation was valid for is
	//: what a stolen or expired delegated key produces.
	if stated.midpoint < delegated.notBefore || stated.midpoint > delegated.notAfter {
		//: Report the refusal.
		return fmt.Errorf("%w: roughtime midpoint outside its delegation window", coreent.ErrCIUnverifiable)
	}
	//: Last and indispensable: this is what makes the answer about us.
	return verifyRoughtimeMerkle(fields, nonce, stated.root)
}

// unframeRoughtime strips the packet header and returns the message inside it.
func unframeRoughtime(packet []byte) (message []byte, err error) {
	header := len(roughtimeFrameMagic) + roughtimeTagSize
	//: Too short to carry a frame, let alone a message.
	if len(packet) < header || string(packet[:len(roughtimeFrameMagic)]) != roughtimeFrameMagic {
		//: Report the malformed answer.
		return nil, fmt.Errorf("%w: roughtime packet is not framed", coreent.ErrCIUnverifiable)
	}
	stated, statedErr := roughtimeUint32(packet[len(roughtimeFrameMagic):], "frame length")
	//: A length we cannot read bounds nothing.
	if statedErr != nil {
		//: Propagate the malformed answer.
		return nil, statedErr
	}
	//: A length longer than what arrived is the classic over-read.
	if int(stated) > len(packet)-header {
		//: Report the malformed answer.
		return nil, fmt.Errorf("%w: roughtime frame claims %d bytes, got %d", coreent.ErrCIUnverifiable, stated, len(packet)-header)
	}
	//: The message, exactly as long as the frame says.
	return packet[header : header+int(stated)], nil
}

// roughtimeDelegation is what a certificate delegates: a key and the window it
// is allowed to speak in.
type roughtimeDelegation struct {
	// key signs the responses this delegation covers.
	key ed25519.PublicKey
	// notBefore and notAfter bound the midpoints it may attest to, in
	// microseconds since the epoch.
	notBefore, notAfter uint64
}

// verifyRoughtimeCert authenticates the delegation against the pinned key.
//
// This is where the pin does its work. Everything after it rests on a key the
// server generated moments ago; the only reason to believe that key belongs to
// the server at all is this signature, made with the half that was pinned into
// the binary by somebody who checked.
func verifyRoughtimeCert(cert []byte, longTerm ed25519.PublicKey) (delegation roughtimeDelegation, err error) {
	fields, decodeErr := decodeRoughtimeMessage(cert)
	//: A certificate we cannot take apart delegates nothing.
	if decodeErr != nil {
		//: Propagate the malformed answer.
		return roughtimeDelegation{}, fmt.Errorf("roughtime certificate: %w", decodeErr)
	}

	dele := fields[roughtimeTag("DELE")]
	//: The pinned key must have signed this exact delegation.
	if sigErr := checkRoughtimeSignature(longTerm, roughtimeDelegationContext, dele, fields[roughtimeTag("SIG")]); sigErr != nil {
		//: Propagate the refusal.
		return roughtimeDelegation{}, fmt.Errorf("roughtime delegation: %w", sigErr)
	}

	inner, innerErr := decodeRoughtimeMessage(dele)
	//: A delegation we cannot take apart carries no key.
	if innerErr != nil {
		//: Propagate the malformed answer.
		return roughtimeDelegation{}, fmt.Errorf("roughtime delegation body: %w", innerErr)
	}
	key := inner[roughtimeTag("PUBK")]
	//: A delegated key of the wrong length would panic ed25519.Verify.
	if len(key) != ed25519.PublicKeySize {
		//: Report the malformed answer.
		return roughtimeDelegation{}, fmt.Errorf("%w: roughtime delegated key is %d bytes", coreent.ErrCIUnverifiable, len(key))
	}

	notBefore, beforeErr := roughtimeUint64(inner[roughtimeTag("MINT")], "MINT")
	notAfter, afterErr := roughtimeUint64(inner[roughtimeTag("MAXT")], "MAXT")
	//: A window we cannot read bounds nothing, so it bounds everything.
	if beforeErr != nil || afterErr != nil {
		//: Report the malformed answer.
		return roughtimeDelegation{}, fmt.Errorf("%w: roughtime delegation window is unreadable", coreent.ErrCIUnverifiable)
	}
	//: A delegation good for everything and nothing.
	return roughtimeDelegation{key: ed25519.PublicKey(key), notBefore: notBefore, notAfter: notAfter}, nil
}

// checkRoughtimeSignature verifies one context-prefixed ed25519 signature.
//
// The context string is what stops a signature made for one purpose being
// replayed as another — a delegation presented as a response, say.
func checkRoughtimeSignature(key ed25519.PublicKey, context string, signed, signature []byte) error {
	//: A signature of the wrong length verifies nothing and must not reach
	//: ed25519.Verify, which is entitled to assume its own sizes.
	if len(signature) != ed25519.SignatureSize {
		//: Report the malformed answer.
		return fmt.Errorf("%w: signature is %d bytes, want %d", coreent.ErrCIUnverifiable, len(signature), ed25519.SignatureSize)
	}
	//: Context first, then the signed bytes: the domain separation IS part of
	//: the message.
	if !ed25519.Verify(key, append([]byte(context), signed...), signature) {
		//: Report the refusal.
		return fmt.Errorf("%w: signature does not verify", coreent.ErrCIUnverifiable)
	}
	//: Authentic.
	return nil
}

// roughtimeStatement is what a signed response asserts.
type roughtimeStatement struct {
	// midpoint is the attested instant, microseconds since the epoch.
	midpoint uint64
	// radius is the server's own uncertainty, in microseconds.
	radius uint32
	// root commits to every nonce the server answered in this batch.
	root []byte
}

// readRoughtimeSrep pulls the three fields a signed response carries.
func readRoughtimeSrep(srep []byte) (stated roughtimeStatement, err error) {
	fields, decodeErr := decodeRoughtimeMessage(srep)
	//: A signed response we cannot take apart asserts nothing.
	if decodeErr != nil {
		//: Propagate the malformed answer.
		return roughtimeStatement{}, fmt.Errorf("roughtime signed response: %w", decodeErr)
	}

	midpoint, midErr := roughtimeUint64(fields[roughtimeTag("MIDP")], "MIDP")
	//: Without the instant there is nothing to have asked for.
	if midErr != nil {
		//: Propagate the malformed answer.
		return roughtimeStatement{}, midErr
	}
	radius, radiusErr := roughtimeUint32(fields[roughtimeTag("RADI")], "RADI")
	//: Without the uncertainty the instant cannot be used honestly.
	if radiusErr != nil {
		//: Propagate the malformed answer.
		return roughtimeStatement{}, radiusErr
	}
	root := fields[roughtimeTag("ROOT")]
	//: A root of the wrong width commits to nothing this client can check.
	if len(root) != roughtimeHashSize {
		//: Report the malformed answer.
		return roughtimeStatement{}, fmt.Errorf("%w: roughtime root is %d bytes, want %d", coreent.ErrCIUnverifiable, len(root), roughtimeHashSize)
	}
	//: An assertion, still to be tied to this request.
	return roughtimeStatement{midpoint: midpoint, radius: radius, root: root}, nil
}

// verifyRoughtimeMerkle proves the nonce this process drew is in the tree the
// signed response committed to.
//
// This is the step that makes the answer about US. A response verified without
// it is a genuine, correctly signed statement about somebody else's request,
// captured and replayed — which is precisely what a nonce exists to prevent and
// precisely what is left if the proof is skipped.
func verifyRoughtimeMerkle(fields map[uint32][]byte, nonce, root []byte) error {
	index, indexErr := roughtimeUint32(fields[roughtimeTag("INDX")], "INDX")
	//: Without our position in the tree the path cannot be walked.
	if indexErr != nil {
		//: Propagate the malformed answer.
		return indexErr
	}
	path := fields[roughtimeTag("PATH")]
	//: A path that is not a whole number of nodes, or deeper than any real
	//: batch, is a shape nobody should walk.
	if len(path)%roughtimeHashSize != 0 || len(path)/roughtimeHashSize > roughtimeMaxPathDepth {
		//: Report the malformed answer.
		return fmt.Errorf("%w: roughtime path is %d bytes", coreent.ErrCIUnverifiable, len(path))
	}

	hash := hashRoughtimeLeaf(nonce)
	//: Climb one level per sibling; the index's low bit says which side we
	//: are on, and it shifts away as we rise.
	for offset := 0; offset < len(path); offset += roughtimeHashSize {
		sibling := path[offset : offset+roughtimeHashSize]
		//: Bit set means we are the RIGHT child.
		if index&1 == 1 {
			hash = hashRoughtimeNode(sibling, hash)
		} else {
			hash = hashRoughtimeNode(hash, sibling)
		}
		index >>= 1
	}
	//: A root we did not reproduce is a tree our nonce is not in.
	if !bytes.Equal(hash, root) {
		//: Report the refusal.
		return fmt.Errorf("%w: roughtime path does not reach the signed root", coreent.ErrCIUnverifiable)
	}
	//: This answer is about this request.
	return nil
}

// hashRoughtimeLeaf hashes a client nonce into a tree leaf.
func hashRoughtimeLeaf(nonce []byte) []byte {
	sum := sha512.Sum512(append([]byte{roughtimeLeafPrefix}, nonce...))
	//: Truncated to the node width the format uses throughout.
	return sum[:roughtimeHashSize]
}

// hashRoughtimeNode hashes two children into their parent.
func hashRoughtimeNode(left, right []byte) []byte {
	joined := make([]byte, 0, 1+len(left)+len(right))
	joined = append(append(append(joined, roughtimeNodePrefix), left...), right...)
	sum := sha512.Sum512(joined)
	//: Truncated to the node width the format uses throughout.
	return sum[:roughtimeHashSize]
}
