package s3_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/baseenc"
	"github.com/kitsunium/sdk/third-party/aws/writer/s3"
)

// Test_s3CodesDoNotCollideWithBaseEnc is the V92/V99 regression: s3 used to
// squat baseenc's PP octet 0x18 (0.3.24.*), making CodeS3ClientInitFailed and
// baseenc.CodeBaseEncUnmarshalFailed the exact same uint32 (0.3.24.2). Under
// HasCode / NewPrefixMatcher routing an S3 client-init failure and a
// base-encoding unmarshal failure were indistinguishable. After re-allocation
// to 0x23 (0.3.35.*) the two packages share no Code and no PP octet. This test
// FAILS on the pre-fix tree (equal values) and PASSES once s3 moves off 0x18.
func Test_s3CodesDoNotCollideWithBaseEnc(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		s3   errs.Code
		base errs.Code
	}
	tests := []tc{
		{"client-init vs baseenc-unmarshal no longer alias", s3.CodeS3ClientInitFailed, baseenc.CodeBaseEncUnmarshalFailed},
		{"s3-put vs baseenc-size share neither value nor octet", s3.CodeS3PutFailed, baseenc.CodeBaseEncSizeExceeded},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a literal value collision conflates the two errors under HasCode.
		if c.s3 == c.base {
			t.Errorf("%s: s3 %s == baseenc %s — V92/V99 collision", c.name, c.s3, c.base)
		}
		//: even a shared PP octet conflates them under a /24 prefix matcher.
		if c.s3.Package() == c.base.Package() {
			t.Errorf("%s: s3 %s and baseenc %s share PP octet 0x%02x", c.name, c.s3, c.base, uint8(c.s3.Package()))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_s3CodesOwnDedicatedOctet is the V95 regression: s3's allocation must sit
// on its own service slot (0x23 / 0.3.35.*) with the real .2 / .20 serials that
// the ADR 0015 registry now lists — not the phantom 0.3.24.* serials ADR 0012
// once recorded. It FAILS while the codes carry the old PP octet and PASSES once
// both s3 codes resolve to 0x23 with their documented serials.
func Test_s3CodesOwnDedicatedOctet(t *testing.T) {
	t.Parallel()
	const wantPP errs.PkgCode = 0x23
	type tc struct {
		name       string
		code       errs.Code
		wantSerial errs.Serial
	}
	tests := []tc{
		{"ClientInit on 0.3.35.2", s3.CodeS3ClientInitFailed, 0x02},
		{"Put on 0.3.35.20", s3.CodeS3PutFailed, 0x14},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: every s3 code must live on the re-allocated PP octet 0x23.
		if c.code.Package() != wantPP {
			t.Errorf("%s: %s has PP 0x%02x want 0x%02x", c.name, c.code, uint8(c.code.Package()), uint8(wantPP))
		}
		//: serials are the ADR-registered .2 / .20 — guards an accidental renumber.
		if c.code.Serial() != c.wantSerial {
			t.Errorf("%s: %s has serial 0x%02x want 0x%02x", c.name, c.code, uint8(c.code.Serial()), uint8(c.wantSerial))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
