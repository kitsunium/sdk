package token_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/crypto/hmacsha2"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// benchValidHeader is a well-formed HS256 JOSE header.
const benchValidHeader string = `{"alg":"HS256","typ":"JWT"}`

// benchValidPayload is a well-formed claim set that expires in 2030, so a row
// whose subject is the HEADER is never also refused for its claims.
const benchValidPayload string = `{"iss":"https://auth.example","sub":"u","exp":1893456000,"iat":1893452400}`

// bench64 is the unpadded base64url encoder every hand-built benchmark token
// uses. It is the encoder the package itself accepts, so a segment built here
// is refused for the reason the row names and never for its alphabet.
var bench64 = base64.RawURLEncoding

// refusalCase is one row of the refusal table: a name, a token, and whether
// the refusal happens before or after the signature was checked.
//
// A SLICE of these rather than a map keyed on the name, because the names are
// sub-benchmark labels: an int-keyed map would give the rows numbers a reader
// has to look up, and a map's iteration order changes between processes, which
// is exactly the kind of run-to-run difference a nine-sample campaign should
// not have to absorb.
type refusalCase struct {
	// name labels the sub-benchmark.
	name string
	// token is the input handed to Verify.
	token string
	// stage is "pre-auth" when the refusal happens before any key material
	// reaches a primitive, "post-auth" when the signature verified first.
	stage string
}

// BenchmarkVerifyRefusal is the security half of this report.
//
// A refusal that costs MORE than the acceptance it replaces is a
// denial-of-service lever: an attacker who cannot forge a token can still
// spend the server's CPU by sending one that is refused expensively. ADR 0042
// states that every bound is checked before the work it funds; this benchmark
// is how that ordering is verified by measurement rather than by reading.
//
// Every row is HS256, deliberately: it is the cheapest acceptance this package
// offers, so a refusal that does not beat HS256 does not beat anything.
func BenchmarkVerifyRefusal(b *testing.B) {
	ring := newBenchKeyring(b)
	clk := clock.NewManualClock(benchEpoch)
	valid := benchMint(b, ring.issuerFor(b, "HS256", clk), benchClaims(b, "realistic"))
	verifier := ring.verifierFor(b, "HS256", clk)
	benchVerifyAccepts(b, verifier, valid)
	for _, row := range benchRefusalCases(b, ring, valid) {
		b.Run(row.stage+"/"+row.name, func(b *testing.B) {
			benchRefuses(b, verifier, row.token)
			b.ReportAllocs()
			b.ReportMetric(float64(len(row.token)), "token_bytes")
			b.ResetTimer()
			for range b.N {
				if _, err := verifier.Verify(row.token); err == nil {
					b.Fatal("this row measures a refusal but the token was accepted")
				}
			}
		})
	}
}

// benchRefuses asserts the token really is refused before it is timed.
//
// The inverse of benchVerifyAccepts, and needed for the same reason: a row
// whose "malformed" token happens to verify would publish an acceptance under
// a refusal's name.
func benchRefuses(b *testing.B, verifier coretoken.Verifier, token string) {
	b.Helper()
	if _, err := verifier.Verify(token); err == nil {
		b.Fatal("token was accepted, so this row would measure an acceptance")
	}
}

// benchRefusalCases builds every refusal input once.
func benchRefusalCases(b *testing.B, ring benchKeyring, valid string) []refusalCase {
	b.Helper()
	return []refusalCase{
		{"bad_signature", benchFlipSignature(b, valid), "post-sig"},
		{"algorithm_none", benchForgeHS256(b, ring, `{"alg":"none","typ":"JWT"}`, benchValidPayload), "pre-auth"},
		{"algorithm_bound", benchForgeHS256(b, ring, `{"alg":"ES256","typ":"JWT"}`, benchValidPayload), "pre-auth"},
		{"segment_count", benchTruncate(valid), "pre-auth"},
		{"bad_base64url", "!!!!." + benchTail(valid), "pre-auth"},
		{"header_too_deep", benchForgeHS256(b, ring, benchNestedJSON(8), benchValidPayload), "pre-auth"},
		{"header_duplicate", benchForgeHS256(b, ring, `{"alg":"HS256","alg":"HS256","typ":"JWT"}`, benchValidPayload), "pre-auth"},
		{"oversized_8_KiB", strings.Repeat("a", svctoken.DefaultMaxTokenLen+1), "pre-auth"},
		{"oversized_1_MiB", strings.Repeat("a", 1<<20), "pre-auth"},
		{"oversized_8_MiB", strings.Repeat("a", 8<<20), "pre-auth"},
		{"claims_duplicate", benchForgeHS256(b, ring, benchValidHeader, `{"sub":"a","sub":"b","exp":1893456000}`), "post-auth"},
		{"claims_too_deep", benchForgeHS256(b, ring, benchValidHeader, benchNestedJSON(24)), "post-auth"},
		//: an expired token is refused by the SAME verifier every other row
		//: uses; moving the manual clock forward would make it a different one.
		{"expired", benchExpiredToken(b, ring), "post-auth"},
	}
}

// benchForgeHS256 assembles a compact token from a header and payload and
// signs it with the real HS256 secret.
//
// The signature is genuine, which is the point: a row measuring a header
// refusal must not also be measuring a signature failure, and a row measuring
// a post-authentication refusal cannot exist at all without one.
func benchForgeHS256(b *testing.B, ring benchKeyring, header, payload string) string {
	b.Helper()
	input := bench64.EncodeToString([]byte(header)) + "." + bench64.EncodeToString([]byte(payload))
	tag := hmacsha2.MAC.Tag(ring.secret, []byte(input))
	return input + "." + bench64.EncodeToString(tag)
}

// benchFlipSignature returns valid with one signature octet changed, so the
// token is structurally perfect and cryptographically wrong.
func benchFlipSignature(b *testing.B, valid string) string {
	b.Helper()
	cut := strings.LastIndexByte(valid, '.')
	raw, err := bench64.DecodeString(valid[cut+1:])
	if err != nil {
		b.Fatalf("decode signature: %v", err)
	}
	raw[0] ^= 0xFF
	return valid[:cut+1] + bench64.EncodeToString(raw)
}

// benchTruncate drops the signature segment, leaving two parts.
func benchTruncate(valid string) string {
	//: everything up to the last separator is header "." payload.
	return valid[:strings.LastIndexByte(valid, '.')]
}

// benchTail returns everything after the first segment of valid.
func benchTail(valid string) string {
	//: the payload and signature, so only the header segment is corrupt.
	return valid[strings.IndexByte(valid, '.')+1:]
}

// benchNestedJSON builds a JSON object nested depth levels deep.
func benchNestedJSON(depth int) string {
	var builder strings.Builder
	//: one object per level, each holding the next under a one-byte name.
	for range depth {
		builder.WriteString(`{"a":`)
	}
	builder.WriteString(`1`)
	//: close every level.
	for range depth {
		builder.WriteString(`}`)
	}
	return builder.String()
}

// benchExpiredToken mints a token that expired before the verifier's clock.
func benchExpiredToken(b *testing.B, ring benchKeyring) string {
	b.Helper()
	past := clock.NewManualClock(benchEpoch.Add(-24 * time.Hour))
	return benchMint(b, ring.issuerFor(b, "HS256", past), benchClaims(b, "realistic"))
}
