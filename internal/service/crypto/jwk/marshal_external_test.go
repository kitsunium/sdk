package jwk_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
)

// parseFixture decodes one of the RFC vectors, failing the test rather than the
// assertion when the fixture itself is wrong.
func parseFixture(tb testing.TB, doc string) jwk.KeyValue {
	tb.Helper()
	key, err := jwk.Parse([]byte(doc))
	//: a broken fixture must not read as a broken assertion.
	if err != nil {
		tb.Fatalf("fixture %s: %v", doc, err)
	}
	return key
}

// TestDefaultSerialisationNeverEmitsPrivateMaterial is the regression guard for
// the reason this package needs a design at all: core/crypto.Key redacts, and a
// JWK serialiser is precisely the function that would undo that. Every path a
// caller can reach WITHOUT typing the word "Private" must come out public.
func TestDefaultSerialisationNeverEmitsPrivateMaterial(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
	}{
		{"private EC key", ecPrivateDoc},
		{"private OKP key", okpPrivateDoc},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
	},
	) {
		t.Helper()
		key := parseFixture(t, c.doc)
		//: the key really does hold the secret — otherwise the test proves
		//: nothing about withholding it.
		if !key.IsPrivate() {
			t.Fatalf("fixture is not private")
		}
		//: three default-ish paths: the named public one, the json.Marshaler
		//: one, and the one a struct field would take.
		viaPublic, perr := key.MarshalPublic()
		viaJSON, jerr := json.Marshal(key)
		viaField, ferr := json.Marshal(struct {
			Key jwk.KeyValue `json:"key"`
		}{Key: key})
		if perr != nil || jerr != nil || ferr != nil {
			t.Fatalf("marshal errors: %v / %v / %v", perr, jerr, ferr)
		}
		for _, out := range []string{string(viaPublic), string(viaJSON), string(viaField)} {
			//: "d" is the private member for both EC and OKP; its mere presence
			//: as a member name is the leak.
			if strings.Contains(out, `"d":`) {
				t.Errorf("private member leaked on a default path: %s", out)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPublicSerialisationRefusesSymmetricKeys pins the other half of the same
// decision: an oct JWK has no public projection, so the public path REFUSES it
// rather than emitting a key-shaped object a caller might publish.
func TestPublicSerialisationRefusesSymmetricKeys(t *testing.T) {
	t.Parallel()
	key := parseFixture(t, octDoc)
	tests := []struct {
		name   string
		invoke func() ([]byte, error)
	}{
		{"MarshalPublic refuses", key.MarshalPublic},
		{"MarshalJSON refuses", key.MarshalJSON},
		{"json.Marshal refuses", func() ([]byte, error) { return json.Marshal(key) }},
		{"Public() refuses", func() ([]byte, error) {
			pub, err := key.Public()
			return []byte(pub.String()), err
		}},
	}
	runCase := func(t *testing.T, c struct {
		name   string
		invoke func() ([]byte, error)
	},
	) {
		t.Helper()
		out, err := c.invoke()
		//: the refusal is typed, so a caller can distinguish it from an
		//: encoding fault and act on it.
		if !errs.HasCode(err, jwk.CodeJWKNoPublicForm) {
			t.Fatalf("err=%v want NoPublicForm", err)
		}
		//: and nothing resembling the secret comes back alongside it.
		if strings.Contains(string(out), `"k":`) {
			t.Errorf("secret member surfaced despite the refusal: %s", out)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestMarshalPrivateIsTheOnlyExport checks the deliberate path does work — a
// guard that only ever refuses would just be a broken package.
func TestMarshalPrivateIsTheOnlyExport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		doc        string
		wantMember string
	}{
		{"EC exports d", ecPrivateDoc, `"d":`},
		{"OKP exports d", okpPrivateDoc, `"d":`},
		{"oct exports k", octDoc, `"k":`},
	}
	runCase := func(t *testing.T, c struct {
		name       string
		doc        string
		wantMember string
	},
	) {
		t.Helper()
		key := parseFixture(t, c.doc)
		out, err := key.MarshalPrivate()
		//: the explicitly named path is the one that emits material.
		if err != nil || !strings.Contains(string(out), c.wantMember) {
			t.Fatalf("MarshalPrivate=(%s,%v) want a %s member", out, err, c.wantMember)
		}
		//: and it round-trips back to an equal key.
		back, perr := jwk.Parse(out)
		if perr != nil || !back.Equal(key) {
			t.Errorf("private round-trip lost the key: %v / %v", back, perr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestMarshalPrivateRefusesAPublicOnlyKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
	}{
		{"public EC key", ecPublicDoc},
		{"public OKP key", okpPublicDoc},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
	},
	) {
		t.Helper()
		out, err := parseFixture(t, c.doc).MarshalPrivate()
		//: refusing beats silently downgrading to the public document, which
		//: would ship a key store that fails at first signature.
		if out != nil || !errs.HasCode(err, jwk.CodeJWKNoPrivateMaterial) {
			t.Errorf("MarshalPrivate=(%s,%v) want NoPrivateMaterial", out, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestZeroKeyIsNotSerialisable(t *testing.T) {
	t.Parallel()
	var zero jwk.KeyValue
	tests := []struct {
		name   string
		invoke func() ([]byte, error)
	}{
		{"MarshalPublic", zero.MarshalPublic},
		{"MarshalPrivate", zero.MarshalPrivate},
	}
	runCase := func(t *testing.T, c struct {
		name   string
		invoke func() ([]byte, error)
	},
	) {
		t.Helper()
		out, err := c.invoke()
		//: the zero value carries no kty, hence nothing to render.
		if out != nil || !errs.HasCode(err, jwk.CodeJWKMissingMember) {
			t.Errorf("%s=(%s,%v) want MissingMember", c.name, out, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestRoundTripIsByteStableForOurOwnOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
	}{
		{"public EC with metadata", ecPublicDoc},
		{"public OKP", okpPublicDoc},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
	},
	) {
		t.Helper()
		first, err := parseFixture(t, c.doc).MarshalPublic()
		if err != nil {
			t.Fatalf("MarshalPublic: %v", err)
		}
		emitted := string(first)
		second, serr := parseFixture(t, emitted).MarshalPublic()
		//: member order comes from the wire struct, so our own documents are
		//: byte-stable across any number of round trips.
		if serr != nil || emitted != string(second) {
			t.Errorf("round trip drifted:\n%s\n%s\n%v", first, second, serr)
		}
		//: the fixtures are written in the emitted member order, so the very
		//: first pass already reproduces them verbatim.
		if emitted != c.doc {
			t.Errorf("re-emitted %s want %s", first, c.doc)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestPublicDropsTheSecretButKeepsMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
	}{
		{"EC private key reduces to its public half", ecPrivateDoc},
		{"OKP private key reduces to its public half", okpPrivateDoc},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
	},
	) {
		t.Helper()
		key := parseFixture(t, c.doc).WithKid("k1").WithUse("sig").WithAlg("ES256")
		pub, err := key.Public()
		if err != nil {
			t.Fatalf("Public: %v", err)
		}
		//: the secret is gone …
		if pub.IsPrivate() {
			t.Errorf("Public() kept the private member")
		}
		//: … and everything that identifies the key is not.
		if pub.Kid() != "k1" || pub.Use() != "sig" || pub.Alg() != "ES256" ||
			pub.Kty() != key.Kty() || pub.Crv() != key.Crv() {
			t.Errorf("Public() dropped metadata: %v", pub)
		}
		//: the receiver is untouched — Key is a value, not a handle.
		if !key.IsPrivate() {
			t.Errorf("Public() mutated the receiver")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFormattingNeverPrintsMaterial pins the other leak surface: not
// serialisation but a log line. core/crypto.Key answers "<redacted>"; a JWK
// carries useful metadata, so it prints that and nothing else.
func TestFormattingNeverPrintsMaterial(t *testing.T) {
	t.Parallel()
	key := parseFixture(t, ecPrivateDoc).WithKid("k1")
	set := jwk.NewSet(key, parseFixture(t, octDoc))
	tests := []struct {
		name    string
		printed string
	}{
		{"%v on a private key", fmt.Sprintf("%v", key)},
		{"String() on a private key", key.String()},
		{"%#v on a private key", fmt.Sprintf("%#v", key)},
		{"%v on a set", fmt.Sprintf("%v", set)},
		{"%#v on a set", fmt.Sprintf("%#v", set)},
		{"%v on a slice of keys", fmt.Sprintf("%v", set.Keys())},
	}
	//: the first octets of the RFC 7515 A.3.1 scalar and of the oct secret, as
	//: fmt would render a []byte — the shape a reflective dump would take.
	leaks := []string{"142 155 16", "[1 1 1 1"}
	runCase := func(t *testing.T, c struct {
		name    string
		printed string
	},
	) {
		t.Helper()
		for _, leak := range leaks {
			//: no formatting verb may reach the material, at any nesting depth.
			if strings.Contains(c.printed, leak) {
				t.Errorf("material leaked through fmt: %s", c.printed)
			}
		}
		//: the rendering must still be useful, not just empty.
		if !strings.Contains(c.printed, "jwk.") {
			t.Errorf("unexpected rendering: %s", c.printed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestThumbprint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
		want string
	}{
		//: RFC 8037 §A.3 publishes this exact thumbprint for this exact key.
		{"RFC 8037 A.3 OKP vector", okpPublicDoc, "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"},
		//: the private key thumbprints to the same value — RFC 7638 hashes the
		//: REQUIRED members only, and "d" is not one of them.
		{"the private key of the same pair agrees", okpPrivateDoc, "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
		want string
	},
	) {
		t.Helper()
		got, err := parseFixture(t, c.doc).Thumbprint()
		//: a known-answer test: the value comes from the RFC, not from us.
		if err != nil || got != c.want {
			t.Errorf("Thumbprint=(%q,%v) want %q", got, err, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestThumbprintIgnoresOptionalMembersAndSeparatesKeys(t *testing.T) {
	t.Parallel()
	ecKey := parseFixture(t, ecPrivateDoc)
	okpKey := parseFixture(t, okpPublicDoc)
	tests := []struct {
		name  string
		left  jwk.KeyValue
		right jwk.KeyValue
		same  bool
	}{
		{"kid does not change the thumbprint", ecKey, ecKey.WithKid("rotated"), true},
		{"use/alg do not change it either", ecKey, ecKey.WithUse("sig").WithAlg("ES256"), true},
		{"different keys thumbprint differently", ecKey, okpKey, false},
	}
	runCase := func(t *testing.T, c struct {
		name  string
		left  jwk.KeyValue
		right jwk.KeyValue
		same  bool
	},
	) {
		t.Helper()
		left, lerr := c.left.Thumbprint()
		right, rerr := c.right.Thumbprint()
		if lerr != nil || rerr != nil {
			t.Fatalf("Thumbprint: %v / %v", lerr, rerr)
		}
		//: a thumbprint identifies the KEY, never the metadata around it.
		if (left == right) != c.same {
			t.Errorf("Thumbprint %q vs %q, want same=%v", left, right, c.same)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestWithThumbprintKid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
	}{
		{"derives a deterministic kid from the key itself", okpPublicDoc},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
	},
	) {
		t.Helper()
		key, err := parseFixture(t, c.doc).WithThumbprintKid()
		if err != nil {
			t.Fatalf("WithThumbprintKid: %v", err)
		}
		want, terr := key.Thumbprint()
		//: the kid must BE the thumbprint — that is what makes it collision
		//: resistant, and therefore what keeps Set.ByKid unambiguous.
		if terr != nil || key.Kid() != want {
			t.Errorf("KID()=%q want %q (%v)", key.Kid(), want, terr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
