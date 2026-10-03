package selfupdate

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreupd "github.com/kitsunium/sdk/framework/internal/core/selfupdate"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// domainStatement prepends the two header lines a signature domain requires.
func domainStatement(tag, expires string, manifest []byte) []byte {
	return []byte("# tag " + tag + "\n# expires " + expires + "\n" + string(manifest))
}

func signDomain(priv ed25519.PrivateKey, domain string, manifest []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, append([]byte(domain+"\x00"), manifest...))))
}

// A release signed by any key of the list verifies; one signed by a key not on
// it does not. The rotation needs nothing more: neither side updates first.
func TestAnyLinkedKeyVerifies(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t, []byte("new"))
	oldPub, _ := vendorKeypair(t)
	handler := func(w http.ResponseWriter, r *http.Request) { f.serveAsset(t, w, r.URL.Path) }
	both := signedServiceFor(t, oldPub, nil, handler).WithVendorKeys(oldPub, f.pub)
	if err := both.verifyArchive("v1.1.0", f.archive); err != nil {
		t.Fatalf("a release signed by the second key of the list: %v", err)
	}
	onlyOld := signedServiceFor(t, oldPub, nil, handler)
	if err := onlyOld.verifyArchive("v1.1.0", f.archive); !errs.HasCode(err, coreupd.CodeSignatureInvalid) {
		t.Fatalf("a release signed by a key not on the list: %v", err)
	}
}

// A list longer than the bound keeps its head; a list of broken keys is no
// anchor at all.
func TestTheKeyListIsBoundedAndCheckedWhole(t *testing.T) {
	t.Parallel()
	pub, _ := vendorKeypair(t)
	u := NewService("v1.0.0", testSource).WithVendorKeys(pub, pub, pub, pub, pub, pub)
	if len(u.vendorKeys) != maxVendorKeys {
		t.Fatalf("kept %d keys, want %d", len(u.vendorKeys), maxVendorKeys)
	}
	broken := NewService("v1.0.0", testSource).WithVendorKeys([]byte("short"), []byte("also short"))
	if err := broken.canAuthenticate("v1.1.0"); !errs.HasCode(err, coreupd.CodeNoVendorKey) {
		t.Fatalf("two broken keys: %v", err)
	}
	mixed := NewService("v1.0.0", testSource).WithVendorKeys([]byte("short"), pub)
	if err := mixed.canAuthenticate("v1.1.0"); err != nil {
		t.Fatalf("one usable key among broken ones: %v", err)
	}
}

// With a domain, a signature over the bare manifest is refused, and a manifest
// must say the tag it is and until when it may be installed.
func TestADomainBindsTheSignatureTheTagAndTheExpiry(t *testing.T) {
	t.Parallel()
	const domain = "kitsunium/statusline/release/v1"
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		statement func(f releaseFixture) []byte
		sign      func(f releaseFixture, m []byte) []byte
		condition string
	}{
		{
			"a valid statement verifies",
			func(f releaseFixture) []byte { return domainStatement("v1.1.0", "2026-10-28T00:00:00Z", f.manifest) },
			func(f releaseFixture, m []byte) []byte { return signDomain(f.priv, domain, m) }, "",
		},
		{
			"a signature without the domain",
			func(f releaseFixture) []byte { return domainStatement("v1.1.0", "2026-10-28T00:00:00Z", f.manifest) },
			func(f releaseFixture, m []byte) []byte { return signManifest(f.priv, m) }, "verify_failed",
		},
		{
			"another domain's signature",
			func(f releaseFixture) []byte { return domainStatement("v1.1.0", "2026-10-28T00:00:00Z", f.manifest) },
			func(f releaseFixture, m []byte) []byte { return signDomain(f.priv, "kitsunium/roster/v1", m) }, "verify_failed",
		},
		{
			"an older release replayed under this tag",
			func(f releaseFixture) []byte { return domainStatement("v1.0.9", "2026-10-28T00:00:00Z", f.manifest) },
			func(f releaseFixture, m []byte) []byte { return signDomain(f.priv, domain, m) }, "tag_mismatch",
		},
		{
			"an expired statement",
			func(f releaseFixture) []byte { return domainStatement("v1.1.0", "2026-09-01T00:00:00Z", f.manifest) },
			func(f releaseFixture, m []byte) []byte { return signDomain(f.priv, domain, m) }, "statement_expired",
		},
		{
			"a statement without its header",
			func(f releaseFixture) []byte { return f.manifest },
			func(f releaseFixture, m []byte) []byte { return signDomain(f.priv, domain, m) }, "statement_incomplete",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newReleaseFixture(t, []byte("new"))
			f.manifest = c.statement(f)
			f.sig = c.sign(f, f.manifest)
			u := signedServiceFor(t, f.pub, nil, func(w http.ResponseWriter, r *http.Request) { f.serveAsset(t, w, r.URL.Path) }).
				WithSignatureDomain(domain).withClock(clock.NewManualClock(now))
			err := u.verifyArchive("v1.1.0", f.archive)
			if c.condition == "" {
				if err != nil {
					t.Fatalf("verifyArchive: %v", err)
				}
				return
			}
			if !errs.HasCode(err, coreupd.CodeSignatureInvalid) || !strings.Contains(diagnose(err)+fieldsOf(err), c.condition) {
				t.Fatalf("verifyArchive = %v (%s), want SIGNATURE_INVALID %s", err, fieldsOf(err), c.condition)
			}
		})
	}
}

// TestTheExpiryIsReadAgainstTheInjectedClock pins that a signed statement's
// expiry is judged by the Service's clock and not by the wall clock. Each case
// puts the injected instant on the OTHER side of the expiry from any real date
// this suite can run on, so the wall clock answers the opposite of what the
// case wants: a statement that expired in 2002 still verifies at an injected
// 2001, and one valid until 2100 is refused at an injected 2101. Seen failing
// with the expiry read from clock.System: both cases, each the other way round.
func TestTheExpiryIsReadAgainstTheInjectedClock(t *testing.T) {
	t.Parallel()
	const domain = "kitsunium/statusline/release/v1"
	cases := []struct {
		name      string
		now       time.Time
		expires   string
		condition string
	}{
		{
			name: "a statement the wall clock calls expired verifies at an earlier injected instant",
			now:  time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), expires: "2002-01-01T00:00:00Z",
		},
		{
			name: "a statement the wall clock calls valid is refused at a later injected instant",
			now:  time.Date(2101, 1, 1, 0, 0, 0, 0, time.UTC), expires: "2100-01-01T00:00:00Z", condition: "statement_expired",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newReleaseFixture(t, []byte("new"))
			f.manifest = domainStatement("v1.1.0", c.expires, f.manifest)
			f.sig = signDomain(f.priv, domain, f.manifest)
			u := signedServiceFor(t, f.pub, nil, func(w http.ResponseWriter, r *http.Request) { f.serveAsset(t, w, r.URL.Path) }).
				WithSignatureDomain(domain).withClock(clock.NewManualClock(c.now))
			err := u.verifyArchive("v1.1.0", f.archive)
			if c.condition == "" {
				if err != nil {
					t.Fatalf("verifyArchive at %s: %v (%s)", c.now.Format(time.RFC3339), err, fieldsOf(err))
				}
				return
			}
			if !errs.HasCode(err, coreupd.CodeSignatureInvalid) || !strings.Contains(fieldsOf(err), c.condition) {
				t.Fatalf("verifyArchive at %s = %v (%s), want SIGNATURE_INVALID %s",
					c.now.Format(time.RFC3339), err, fieldsOf(err), c.condition)
			}
		})
	}
}

// fieldsOf renders an error's fields, for asserting a condition.
func fieldsOf(err error) string {
	var b strings.Builder
	for _, f := range errs.FieldsOf(err) {
		b.WriteString(f.Key() + "=" + f.StringValue() + " ")
	}
	return b.String()
}

// A probe that fails puts the previous binary back from <binary>.prev; one
// that passes leaves the new binary and the previous one beside it.
func TestAFailedProbePutsThePreviousBinaryBack(t *testing.T) {
	t.Parallel()
	for _, pass := range []bool{true, false} {
		dir := t.TempDir()
		exe := filepath.Join(dir, "statusline")
		if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(dir, "staged")
		if err := os.WriteFile(tmp, []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		u := NewService("v1.0.0", testSource).WithProbe([]string{"version"}, time.Second)
		var probed []string
		u.probe.run = func(_ context.Context, path string, args []string) error {
			probed = append([]string{path}, args...)
			if pass {
				return nil
			}
			return errs.NewRuntime(0x00_03_42_FF, "PROBE_TEST", "the new binary crashed", "test")
		}
		err := u.finalizeReplacement(tmp, exe)
		got, readErr := os.ReadFile(exe)
		if readErr != nil {
			t.Fatal(readErr)
		}
		prev, prevErr := os.ReadFile(exe + prevSuffix)
		if prevErr != nil && pass {
			t.Fatal(prevErr)
		}
		if strings.Join(probed, " ") != exe+" version" {
			t.Errorf("the probe ran %q", probed)
		}
		switch {
		case pass && (err != nil || string(got) != "new" || string(prev) != "old"):
			t.Errorf("a passing probe: err=%v binary=%q prev=%q", err, got, prev)
		case !pass && (!errs.HasCode(err, CodeProbeFailed) || string(got) != "old" || !strings.Contains(fieldsOf(err), "rolled_back=true")):
			t.Errorf("a failing probe: err=%v (%s) binary=%q", err, fieldsOf(err), got)
		}
	}
}

// A product that updates silently consents at build; an operator's explicit
// refusal still wins, and the consent grants no elevation.
func TestAutomaticConsentIsTheProductsAndGrantsNothingElse(t *testing.T) {
	src := SourceValue{Product: "statusline-consent-test"}
	u := NewService("v1.0.0", src).WithAutomaticConsent().WithoutElevation()
	if !u.AuthoriseUnattendedUpgrade(nil, nil, false) {
		t.Error("an automatic product did not consent")
	}
	if NewService("v1.0.0", src).AuthoriseUnattendedUpgrade(nil, nil, false) {
		t.Error("a product that did not declare consent consented")
	}
	t.Setenv(src.AutoUpgradeEnv(), "0")
	if u.AuthoriseUnattendedUpgrade(nil, nil, false) {
		t.Error("an operator's explicit refusal was overruled")
	}
	t.Setenv(src.SudoOptInEnv(), "1")
	if err := u.elevate("/nonexistent/a", "/nonexistent/b"); !errs.HasCode(err, coreupd.CodeElevationNotAuthorised) {
		t.Errorf("a product that never escalates escalated: %v", err)
	}
}

// withClock sets the clock the expiry is read against.
func (u *Service) withClock(c clock.Clock) *Service {
	u.clock = c
	return u
}
