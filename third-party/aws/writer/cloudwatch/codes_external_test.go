package cloudwatch_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/third-party/aws/writer/cloudwatch"
)

// TestV95RegistryAlignment pins the cloudwatch sentinels to the canonical
// ADR 0006/0015 numeric registry: the writer owns PP octet 0x19 (25) with
// serial .2 for ClientInit and serial .20 for Put. The phantom FlushFailed=.30
// / CloseFailed=.40 serials from ADR 0012's draft table were never minted; this
// guards against a regression that re-introduces drift between the registry
// prose and the shipped codes.
func TestV95RegistryAlignment(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		code       errs.Code
		wantDotted string
		wantReason string
		sentinel   error
	}
	tests := []tc{
		//: ClientInit lives at serial .2 — the row ADR 0012 omitted entirely.
		{"ClientInit is 0.3.25.2", cloudwatch.CodeCWClientInitFailed, "0.3.25.2", "CLIENT_INIT_FAILED", cloudwatch.ClientInitFailed},
		//: Put lives at serial .20, not the never-minted .30/.40.
		{"Put is 0.3.25.20", cloudwatch.CodeCWPutFailed, "0.3.25.20", "PUT_FAILED", cloudwatch.PutFailed},
		//: EventRejected (V85 per-event window drop) lives at serial .21.
		{"EventRejected is 0.3.25.21", cloudwatch.CodeCWEventRejected, "0.3.25.21", "EVENT_REJECTED", cloudwatch.EventRejected},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the const must render as the canonical dotted-quad the registry lists.
		if got := c.code.String(); got != c.wantDotted {
			t.Errorf("%s: code=%s want %s", c.name, got, c.wantDotted)
		}
		//: the sentinel must route by its registry code (no .Error() matching).
		if !errs.HasCode(c.sentinel, c.code) {
			t.Errorf("%s: HasCode(%s)=false", c.name, c.wantDotted)
		}
		//: and by its declared Reason, proving code↔reason stay paired.
		if !errs.HasReason(c.sentinel, c.wantReason) {
			t.Errorf("%s: HasReason(%s)=false", c.name, c.wantReason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
