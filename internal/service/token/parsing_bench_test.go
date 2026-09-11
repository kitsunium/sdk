package token

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// BenchmarkParsePiece prices each function on the unauthenticated path
// separately, so the "parse" stage of BenchmarkVerifyStage can be attributed
// rather than reported as one opaque number.
//
// Every function here runs on attacker-controlled bytes before any key is
// touched, which is exactly why it is worth knowing what each one costs.
func BenchmarkParsePiece(b *testing.B) {
	rig := newStageRig(b)
	header := parseHeaderBytes(b, rig.token)
	payload := decodeOrFail(b, segmentAt(rig.token, 1), rig.policy.maxTokenLen)
	b.Run("splitCompact", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			seg, err := splitCompact(rig.token, rig.policy.maxTokenLen)
			if err != nil {
				b.Fatalf("splitCompact: %v", err)
			}
			sinkSegments = seg
		}
	})
	b.Run("decodeSegment_header", func(b *testing.B) {
		segment := segmentAt(rig.token, 0)
		b.ReportAllocs()
		for range b.N {
			raw, err := decodeSegment(segment, maxHeaderLen)
			if err != nil {
				b.Fatalf("decodeSegment: %v", err)
			}
			sinkBytes = raw
		}
	})
	b.Run("parseJOSEHeader", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			parsed, err := parseJOSEHeader(header)
			if err != nil {
				b.Fatalf("parseJOSEHeader: %v", err)
			}
			sinkHeader = parsed
		}
	})
	benchmarkClaimPieces(b, rig, payload)
}

// benchmarkClaimPieces prices the three passes decodeClaims runs plus the
// decode and the validation, over an authenticated payload.
func benchmarkClaimPieces(b *testing.B, rig stageRig, payload []byte) {
	b.Helper()
	claims, cerr := decodeClaims(payload, rig.policy.maxClaimDepth, joseShape{})
	if cerr != nil {
		b.Fatalf("decodeClaims: %v", cerr)
	}
	b.Run("checkJSONDepth", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if err := checkJSONDepth(payload, rig.policy.maxClaimDepth); err != nil {
				b.Fatalf("checkJSONDepth: %v", err)
			}
		}
	})
	b.Run("checkNoDuplicateMembers", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if err := checkNoDuplicateMembers(payload); err != nil {
				b.Fatalf("checkNoDuplicateMembers: %v", err)
			}
		}
	})
	b.Run("decodeClaims", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			decoded, err := decodeClaims(payload, rig.policy.maxClaimDepth, joseShape{})
			if err != nil {
				b.Fatalf("decodeClaims: %v", err)
			}
			sinkClaims = decoded
		}
	})
	b.Run("validateClaims", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if err := rig.policy.validateClaims(claims); err != nil {
				b.Fatalf("validateClaims: %v", err)
			}
		}
	})
	b.Run("encodeClaims", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			raw, err := encodeClaims(claims, joseShape{})
			if err != nil {
				b.Fatalf("encodeClaims: %v", err)
			}
			sinkBytes = raw
		}
	})
}

// segmentAt returns the nth dot-separated segment of tok.
func segmentAt(tok string, index int) string {
	//: walk the separators rather than splitting, so this helper allocates
	//: nothing and cannot itself appear in an allocation profile.
	for range index {
		tok = tok[strings.IndexByte(tok, '.')+1:]
	}
	cut := strings.IndexByte(tok, '.')
	//: the last segment has no trailing separator.
	if cut < 0 {
		return tok
	}
	return tok[:cut]
}

// decodeOrFail decodes one base64url segment or fails the benchmark.
func decodeOrFail(b *testing.B, segment string, maxLen int) []byte {
	b.Helper()
	raw, err := decodeSegment(segment, maxLen)
	if err != nil {
		b.Fatalf("decodeSegment: %v", err)
	}
	return raw
}

// parseHeaderBytes returns the decoded JOSE header of tok.
func parseHeaderBytes(b *testing.B, tok string) []byte {
	b.Helper()
	return decodeOrFail(b, segmentAt(tok, 0), maxHeaderLen)
}

// BenchmarkSplitCompactOversized is the CVE-2025-30204 measurement.
//
// splitCompact checks len(tok) against the bound BEFORE it scans. If that
// ordering holds, refusing a token costs the same whether it is one byte over
// the bound or a thousand times over it, because no byte of the payload is
// ever read. If the ordering were reversed — scan, then check — the cost would
// grow with the input and an attacker would have a lever priced in megabytes.
//
// The three rows must be flat to within the noise floor. A rising column here
// is the defect this benchmark exists to catch.
func BenchmarkSplitCompactOversized(b *testing.B) {
	//: the hostile strings are built ONCE, outside b.Run. testing calls a
	//: benchmark body repeatedly while it calibrates b.N, so a 64 MiB
	//: strings.Repeat inside the body would be a dozen 64 MiB allocations
	//: charged to a machine whose balloon the host can halve mid-run.
	inputs := map[string]string{}
	for name, size := range map[string]int{
		"8_KiB+1": DefaultMaxTokenLen + 1,
		"1_MiB":   1 << 20,
		"8_MiB":   8 << 20,
		"64_MiB":  64 << 20,
	} {
		//: no separator at all, which is the shape that forces the scan: the
		//: all-dots token CVE-2025-30204 was reported with is short-circuited
		//: three bytes in by the segment-count check, bound or no bound.
		inputs[name] = strings.Repeat("a", size)
	}
	for name, hostile := range inputs {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ReportMetric(float64(len(hostile)), "token_bytes")
			b.ResetTimer()
			for range b.N {
				if _, err := splitCompact(hostile, DefaultMaxTokenLen); err == nil {
					b.Fatal("an oversized token was accepted")
				}
			}
		})
	}
}

// pasetoStageRig is the PASETO v4.public white-box apparatus.
type pasetoStageRig struct {
	// verifier is the concrete v4.public verifier.
	verifier *pasetoVerifier
	// token is the compact token every stage starts from.
	token string
	// message / footer are that token already split and decoded.
	message, footer []byte
	// signature is the trailing Ed25519 signature of message.
	signature []byte
}

// newPasetoStageRig builds the v4.public apparatus over a realistic claim set.
func newPasetoStageRig(b *testing.B) pasetoStageRig {
	b.Helper()
	pub, priv, gerr := ed25519.GenerateKey(nil)
	if gerr != nil {
		b.Fatalf("GenerateKey: %v", gerr)
	}
	clk := clock.NewManualClock(stageEpoch)
	issuer, ierr := NewPasetoV4Issuer(priv, PasetoIssuerConfig{
		Issuer: stageIssuer, Lifetime: time.Hour, Clock: clk,
	})
	if ierr != nil {
		b.Fatalf("NewPasetoV4Issuer: %v", ierr)
	}
	token, merr := issuer.Issue(stageClaims(b))
	if merr != nil {
		b.Fatalf("Issue: %v", merr)
	}
	verifier, verr := NewPasetoV4Verifier(pub, PasetoVerifierConfig{Issuer: stageIssuer, Clock: clk})
	if verr != nil {
		b.Fatalf("NewPasetoV4Verifier: %v", verr)
	}
	concrete, _ := verifier.(*pasetoVerifier)
	payload, footer, serr := concrete.split(token)
	if serr != nil {
		b.Fatalf("split: %v", serr)
	}
	return pasetoStageRig{
		verifier: concrete, token: token, footer: footer,
		message:   payload[:len(payload)-pasetoSigLen],
		signature: payload[len(payload)-pasetoSigLen:],
	}
}

// BenchmarkPasetoStage is BenchmarkVerifyStage's counterpart for v4.public.
//
// The shapes differ in one way worth measuring: PASETO's signature covers a
// pre-authentication encoding that has to be BUILT on every verification,
// where JWS signs a prefix of the token itself and therefore builds nothing.
// The preAuthEncode row is that difference, priced.
func BenchmarkPasetoStage(b *testing.B) {
	rig := newPasetoStageRig(b)
	b.Run("1_split", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			payload, footer, err := rig.verifier.split(rig.token)
			if err != nil {
				b.Fatalf("split: %v", err)
			}
			sinkBytes, sinkFooter = payload, footer
		}
	})
	b.Run("2_preAuthEncode", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			sinkBytes = preAuthEncode([]byte(pasetoV4PublicHeader), rig.message,
				rig.footer, rig.verifier.cfg.ImplicitAssertion)
		}
	})
	b.Run("3_signature", func(b *testing.B) {
		input := preAuthEncode([]byte(pasetoV4PublicHeader), rig.message,
			rig.footer, rig.verifier.cfg.ImplicitAssertion)
		b.ReportAllocs()
		for range b.N {
			if !rig.verifier.bound.verify(input, rig.signature) {
				b.Fatal("signature did not verify")
			}
		}
	})
	b.Run("4_decodeAuthenticated", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			claims, err := rig.verifier.decodeAuthenticated(rig.message)
			if err != nil {
				b.Fatalf("decodeAuthenticated: %v", err)
			}
			sinkClaims = claims
		}
	})
	b.Run("whole", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			claims, err := rig.verifier.Verify(rig.token)
			if err != nil {
				b.Fatalf("Verify: %v", err)
			}
			sinkClaims = claims
		}
	})
}

// skipValueByCopy is the implementation skipValue's doc comment says it
// replaced: decode the value into a json.RawMessage and throw it away.
//
// It exists ONLY here, in the benchmark file, so the claim in that comment —
// "a memory profile put that copy at 23 % of the objects a token verification
// allocates" — is reproducible rather than folklore. Nothing in production
// calls it.
func skipValueByCopy(dec *json.Decoder) error {
	var discarded json.RawMessage
	//: the obvious spelling, and the one that copies the whole value.
	return dec.Decode(&discarded)
}

// checkNoDuplicateMembersByCopy is checkNoDuplicateMembers with the copying
// skip, so the two can be measured over identical input.
func checkNoDuplicateMembersByCopy(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, terr := dec.Token()
	//: consume the opening brace.
	if terr != nil || open != json.Delim('{') {
		return coretoken.Malformed
	}
	seen := map[string]struct{}{}
	//: walk member names, copying each value wholesale.
	for dec.More() {
		name, nerr := dec.Token()
		key, isString := name.(string)
		//: a member name is always a string.
		if nerr != nil || !isString {
			return coretoken.Malformed
		}
		//: the duplicate check itself is unchanged.
		if _, dup := seen[key]; dup {
			return DuplicateMember
		}
		seen[key] = struct{}{}
		//: the one difference: the value is copied, then discarded.
		if serr := skipValueByCopy(dec); serr != nil {
			return coretoken.Malformed
		}
	}
	return nil
}

// BenchmarkDuplicateScan prices the RFC 8725 §2.6 duplicate-member refusal,
// and the difference between the two ways of writing its value skip.
//
// The value size is the axis, because it is the axis the two implementations
// disagree on: skipValue walks tokens and copies nothing, so its cost is the
// SCAN; skipValueByCopy allocates a json.RawMessage per member, so its cost is
// the scan PLUS the payload. A caller whose token carries one large private
// claim pays that difference on every request.
func BenchmarkDuplicateScan(b *testing.B) {
	for _, size := range []int{16, 256, 4096} {
		payload := []byte(`{"iss":"https://auth.example","sub":"u","exp":1893456000,"blob":"` +
			strings.Repeat("x", size) + `"}`)
		benchmarkScan(b, "skip/value="+itoaBench(size), payload, checkNoDuplicateMembers)
		benchmarkScan(b, "copy/value="+itoaBench(size), payload, checkNoDuplicateMembersByCopy)
	}
}

// benchmarkScan runs one duplicate-member implementation over one payload.
//
// The payload arrives as an ARGUMENT rather than through a closure capture, so
// the loop variable it comes from does not escape to the heap — which would
// put an allocation the benchmark did not intend inside the window it reports.
func benchmarkScan(b *testing.B, name string, payload []byte, scan func([]byte) error) {
	b.Helper()
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(float64(len(payload)), "payload_bytes")
		for range b.N {
			if err := scan(payload); err != nil {
				b.Fatalf("%s: %v", name, err)
			}
		}
	})
}

// itoaBench renders a small non-negative int for a sub-benchmark name.
func itoaBench(n int) string {
	//: the only zero this is ever asked for.
	if n == 0 {
		return "0"
	}
	var digits []byte
	//: least significant first, then reversed.
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// BenchmarkDuplicateScanRealistic runs both duplicate-member implementations
// over the EXACT two payloads one realistic HS256 verification scans: the JOSE
// header and the claim set.
//
// A verification calls checkNoDuplicateMembers twice, so the share skipValue
// saves is the sum of both deltas — not one of them extrapolated. This row set
// exists because encoding.go's own comment quotes a percentage of "an HS256
// verification" and there was no benchmark in the tree that could produce one.
func BenchmarkDuplicateScanRealistic(b *testing.B) {
	rig := newStageRig(b)
	payloads := map[string][]byte{
		"header": parseHeaderBytes(b, rig.token),
		"claims": decodeOrFail(b, segmentAt(rig.token, 1), rig.policy.maxTokenLen),
	}
	for _, name := range []string{"header", "claims"} {
		benchmarkScan(b, "skip/"+name, payloads[name], checkNoDuplicateMembers)
		benchmarkScan(b, "copy/"+name, payloads[name], checkNoDuplicateMembersByCopy)
	}
}
