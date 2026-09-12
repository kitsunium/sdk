// Package entitlement - Roughtime: an Ed25519-signed statement of the current time,
// bound to a nonce this process just drew.
//
// What it is FOR, stated narrowly because the honest scope is narrow. The clock
// ratchet in cache.go catches a clock moving backwards past vendor-signed
// evidence; it cannot catch a clock that is simply wrong in a direction the
// ratchet never sees, and its resolution is the roster's publication interval.
// A signed timestamp bound to a fresh nonce answers both, and it is the only
// shape that does: an unsigned source (an NTP reply, an HTTPS Date header) is
// forged by anyone on the path, and a signed source without a nonce is replayed
// from a capture.
//
// What it is NOT. It needs the network, so it contributes nothing in the case
// the offline grace window exists for — and an adversary who can substitute an
// origin can also drop UDP to a nonstandard port, which is the entire cost of
// evading it. It is therefore ADVISORY: unreachable or unverifiable means no
// signal and the verification proceeds, because refusing a legitimate user
// whose network blocks port 2002 would trade a real cost for a defence that
// attacker never has to face.
//
// It also never advances the ratchet, and that restriction is load-bearing. A
// response this package accepted in error could otherwise pin the high-water
// mark into the future and refuse the machine permanently. The only thing a
// Roughtime answer can do is refuse a clock that disagrees with it NOW.
//
// # Interoperability is unverified
//
// The constants below are taken from the Roughtime draft, and this client has
// never completed a handshake with a live server: UDP/2002 is filtered on the
// network this was written on, and three independent servers were probed with
// both protocol variants for a total of zero replies. The verifier is tested
// against a server fixture in this package, which proves it is self-consistent
// and NOT that it speaks the same dialect as Cloudflare or Netnod.
//
// RoughtimeServers is empty in committed source for exactly that reason, in the
// same spirit as vendorPublicKeyB64: the mechanism ships, the anchor does not.
// Populating it is a deliberate act by somebody who has watched this client
// verify a real response, and until then every path here is inert.
package entitlement

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// Roughtime wire constants.
const (
	// roughtimeFrameMagic prefixes every packet in the framed dialect.
	roughtimeFrameMagic string = "ROUGHTIM"
	// roughtimeVersion is the protocol version this client advertises.
	roughtimeVersion uint32 = 1
	// roughtimeNonceSize is the challenge length. The nonce is what makes a
	// captured response worthless: the server signs a tree containing it.
	roughtimeNonceSize int = 32
	// roughtimeMinRequest is the floor every request is padded to. It is an
	// anti-amplification rule of the protocol, not a preference: a server is
	// entitled to drop anything smaller, which is one reason a client that
	// forgets it sees perfect silence.
	roughtimeMinRequest int = 1024
	// roughtimeMaxResponse caps what is read back, for the same reason every
	// other endpoint in this package is capped.
	roughtimeMaxResponse int = 4096
	// roughtimeMaxPathDepth bounds the Merkle proof. A tree deeper than this
	// would hold more requests than a server batches in a second, and the
	// depth is attacker-chosen.
	roughtimeMaxPathDepth int = 32
	// roughtimeHashSize is the width of a Merkle node: SHA-512 truncated.
	roughtimeHashSize int = 32
	// roughtimeTimeout bounds one query. Short on purpose — this is a
	// corroborating signal on a start-up path, and a user must never wait on
	// it. A filtered port is the common case, and it costs exactly this.
	roughtimeTimeout time.Duration = 2 * time.Second
)

// Roughtime signature contexts, NUL-terminated as the draft specifies.
//
// These are the two values an interoperability failure is most likely to hide
// in: they are hashed into every signature, so a wrong byte here produces a
// client that verifies nothing at all while looking entirely healthy. They are
// named as constants rather than inlined so that whoever validates against a
// real server has one place to correct.
const (
	// roughtimeDelegationContext prefixes the long-term key's signature over
	// the delegation.
	roughtimeDelegationContext string = "RoughTime v1 delegation signature--\x00"
	// roughtimeResponseContext prefixes the delegated key's signature over the
	// signed response.
	roughtimeResponseContext string = "RoughTime v1 response signature\x00"
)

// maxClockSkew is how far the local clock may sit from signed network time
// before it is refused.
//
// Generous on purpose. What this is aimed at is a clock wrong by hours or days
// — a dead CMOS battery, a restored snapshot, a deliberate rollback — and a
// tight bound would turn ordinary drift, a slow link and a server's own radius
// into refusals for machines that are perfectly fine.
const maxClockSkew time.Duration = 5 * time.Minute

// RoughtimeServerValue is one server this client will ask, and the key it must
// answer with.
//
// The public key is the whole trust decision: a server is not trusted because
// of where it is, it is trusted because a statement carries its signature. That
// is the same rule the roster follows, with a different anchor.
type RoughtimeServerValue struct {
	// Name identifies the server in diagnostics.
	Name string
	// Address is "host:port", reached over UDP.
	Address string
	// PublicKey is the server's long-term ed25519 public half, which signs
	// the delegation that in turn signs each response.
	PublicKey ed25519.PublicKey
}

// RoughtimeServers is the list this client queries, EMPTY in committed source.
//
// Empty means the network-time check does nothing, which is the correct state
// until somebody has watched this client verify a live response: an entry added
// on the strength of an untested implementation would either be inert anyway or
// — worse — start refusing machines over a parsing bug. Adding one is the same
// kind of decision as stamping the vendor anchor, and belongs to whoever can
// confirm it works.
var RoughtimeServers []RoughtimeServerValue

// QueryRoughtime asks one server what time it is and verifies the answer.
//
// The nonce is drawn here rather than taken from the caller: freshness is the
// only thing separating this from a replayed capture, and a caller that could
// supply the nonce could supply yesterday's.
func QueryRoughtime(server RoughtimeServerValue) (midpoint time.Time, radius time.Duration, err error) {
	//: A server with no key cannot be checked, and a check that cannot run
	//: must never pass.
	if len(server.PublicKey) != ed25519.PublicKeySize {
		//: Report it as unverifiable; the caller carries on regardless.
		return time.Time{}, 0, fmt.Errorf("%w: roughtime server %q has no usable key", coreent.ErrCIUnverifiable, server.Name)
	}

	var nonce [roughtimeNonceSize]byte
	//: Without entropy the challenge is predictable and proves nothing.
	if _, randErr := rand.Read(nonce[:]); randErr != nil {
		//: Report it as unverifiable.
		return time.Time{}, 0, fmt.Errorf("%w: drawing roughtime nonce: %w", coreent.ErrCIUnverifiable, randErr)
	}

	raw, exchangeErr := roughtimeExchange(server.Address, buildRoughtimeRequest(nonce[:]))
	//: An answer we did not get proves nothing.
	if exchangeErr != nil {
		//: Propagate the transport failure.
		return time.Time{}, 0, exchangeErr
	}
	//: Everything from here is signature checking over bytes the server chose.
	return verifyRoughtimeResponse(raw, nonce[:], server.PublicKey)
}

// buildRoughtimeRequest assembles the padded request packet.
//
// The padding is not decoration: a request under the protocol's floor is
// dropped without a reply, so getting this wrong looks exactly like a filtered
// port.
func buildRoughtimeRequest(nonce []byte) []byte {
	var version [roughtimeTagSize]byte
	//: The version list, one entry long, little-endian like every other
	//: number on this wire.
	binary.LittleEndian.PutUint32(version[:], roughtimeVersion)

	//: Tags ascend as little-endian uint32: VER < NONC < ZZZZ.
	fields := []roughtimeField{
		{tag: roughtimeTag("VER"), value: version[:]},
		{tag: roughtimeTag("NONC"), value: nonce},
		{tag: roughtimeTag("ZZZZ"), value: nil},
	}
	//: Size the padding so the FRAMED packet clears the floor, not the
	//: message inside it — the frame counts.
	overhead := len(roughtimeFrameMagic) + roughtimeTagSize + len(encodeRoughtimeMessage(fields))
	//: bytes.Repeat and not make([]byte, n): this is a fixed run of zeros
	//: that is appended FROM, never appended TO, and spelling it as a
	//: length-sized make reads to KTN-VAR-SLICECAP as a buffer about to grow.
	//: The primitive that says "n zero bytes" says it without the ambiguity.
	fields[2].value = bytes.Repeat([]byte{0}, max(roughtimeMinRequest-overhead, 0))

	message := encodeRoughtimeMessage(fields)
	packet := make([]byte, 0, len(roughtimeFrameMagic)+roughtimeTagSize+len(message))
	packet = append(packet, roughtimeFrameMagic...)
	packet = binary.LittleEndian.AppendUint32(packet, uint32(len(message)))
	//: One packet, framed and padded.
	return append(packet, message...)
}

// roughtimeExchange sends one datagram and reads one back, under a bound.
func roughtimeExchange(address string, request []byte) (response []byte, err error) {
	conn, dialErr := net.Dial("udp", address)
	//: An address we cannot reach answers nothing.
	if dialErr != nil {
		//: Report it as unverifiable.
		return nil, fmt.Errorf("%w: dialling roughtime %s: %w", coreent.ErrCIUnverifiable, address, dialErr)
	}
	//: The body of closeBestEffort, inlined: KTN-GOROUTINE-DEFER recognises
	//: `x.Close()` and a func literal containing it, never `helper(x)`, and
	//: its intent — release the socket where it is acquired — is right.
	defer func() {
		//: Best-effort: a socket we cannot release is worth a line, never a
		//: refused licence.
		if closeErr := conn.Close(); closeErr != nil {
			log.Printf("close %s: %v", address, closeErr)
		}
	}()

	//: One deadline covers both halves; a server that never answers is the
	//: ordinary case on a filtered network and must not hang a start-up.
	if deadlineErr := conn.SetDeadline(time.Now().Add(roughtimeTimeout)); deadlineErr != nil {
		//: Report it as unverifiable.
		return nil, fmt.Errorf("%w: roughtime deadline: %w", coreent.ErrCIUnverifiable, deadlineErr)
	}
	//: A request we could not send earns no answer.
	if _, writeErr := conn.Write(request); writeErr != nil {
		//: Report it as unverifiable.
		return nil, fmt.Errorf("%w: roughtime request: %w", coreent.ErrCIUnverifiable, writeErr)
	}

	buffer := make([]byte, roughtimeMaxResponse)
	read, readErr := conn.Read(buffer)
	//: Silence is what a filtered port and a dropped request look like alike.
	if readErr != nil {
		//: Report it as unverifiable.
		return nil, fmt.Errorf("%w: roughtime response: %w", coreent.ErrCIUnverifiable, readErr)
	}
	//: Whatever arrived, entirely untrusted.
	return buffer[:read], nil
}

// checkNetworkTime refuses a local clock that disagrees with a signed network
// time by more than maxClockSkew.
//
// FAIL-OPEN, and deliberately so. No server configured, none reachable, none
// whose answer verifies: all three return nil and the verification carries on.
// An adversary drops UDP to a nonstandard port for free, so failing closed
// would buy nothing against them while refusing every legitimate user behind a
// firewall that does the same thing by policy — a real cost for an imaginary
// defence.
//
// The first server that answers verifiably decides. Asking the rest would only
// add latency to a start-up path: each one is pinned to its own key, so a
// second opinion is not more trustworthy, just slower.
func (s *Service) checkNetworkTime(now time.Time) error {
	//: Committed source configures none, which is the ordinary state.
	if len(s.timeServers) == 0 {
		//: No signal, no opinion.
		return nil
	}

	//: The first verifiable answer wins; the rest are latency.
	for _, server := range s.timeServers {
		midpoint, radius, queryErr := QueryRoughtime(server)
		//: Unreachable or unverifiable is no signal at all, not a refusal.
		if queryErr != nil {
			//: Try the next server.
			continue
		}
		//: The server's own uncertainty widens the tolerance rather than
		//: narrowing it: refusing inside a window it told us it was unsure
		//: about would be refusing on our own arithmetic.
		if skew := now.Sub(midpoint).Abs(); skew > maxClockSkew+radius {
			//: Name both readings; "set the clock" needs a target.
			return fmt.Errorf("%w: clock reads %s, %s attests %s",
				coreent.ErrClockRegressed,
				now.UTC().Format(time.RFC3339),
				server.Name,
				midpoint.Format(time.RFC3339))
		}
		//: Corroborated.
		return nil
	}
	//: Nobody answered verifiably; that is not evidence of anything.
	return nil
}
