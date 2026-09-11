package jwk_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
)

// setDoc wraps JWK documents in the RFC 7517 §5 envelope.
func setDoc(members ...string) string {
	return `{"keys":[` + strings.Join(members, ",") + `]}`
}

func TestParseSetAccepts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
		want int
	}{
		{"an empty array is an empty set", `{"keys":[]}`, 0},
		{"a single public key", setDoc(ecPublicDoc), 1},
		{"mixed families in one set", setDoc(ecPublicDoc, okpPublicDoc, octDoc), 3},
		{"unknown envelope members are ignored", `{"keys":[` + okpPublicDoc + `],"issuer":"x"}`, 1},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
		want int
	},
	) {
		t.Helper()
		set, err := jwk.ParseSet([]byte(c.doc))
		//: a valid set document decodes every member.
		if err != nil || set.Len() != c.want {
			t.Errorf("ParseSet=(%d keys,%v) want %d", set.Len(), err, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestParseSetRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		doc      string
		wantCode errs.Code
	}{
		{"keys member absent", `{"issuer":"x"}`, jwk.CodeJWKMissingMember},
		{"keys member null", `{"keys":null}`, jwk.CodeJWKMissingMember},
		//: a null ENVELOPE is not an object at all, and neither is a null
		//: member; both read as MISSING_MEMBER before.
		{"the envelope is null", `null`, jwk.CodeJWKMalformed},
		{"a member is null", `{"keys":[null]}`, jwk.CodeJWKMalformed},
		{"keys member is not an array", `{"keys":{}}`, jwk.CodeJWKMalformed},
		{"a member is not an object", `{"keys":["EC"]}`, jwk.CodeJWKMalformed},
		//: a bad member keeps ITS OWN code through the set wrapper — origin
		//: wins, so a caller still learns the curve was the problem.
		{"a member fails its own validation", setDoc(`{"kty":"EC","crv":"P-192","x":"` + ecXCoord + `","y":"` + ecXCoord + `"}`), jwk.CodeJWKUnsupportedCurve},
		{"a member is off-curve", setDoc(`{"kty":"EC","crv":"P-256","x":"` + ecXCoord + `","y":"` + ecXCoord + `"}`), jwk.CodeJWKKeyMismatch},
	}
	runCase := func(t *testing.T, c struct {
		name     string
		doc      string
		wantCode errs.Code
	},
	) {
		t.Helper()
		set, err := jwk.ParseSet([]byte(c.doc))
		//: a rejected document yields the zero Set, never a partial one.
		if set.Len() != 0 {
			t.Errorf("ParseSet kept %d keys on rejection", set.Len())
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Errorf("ParseSet err=%v want code %v", err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestByKidIsUnambiguousOrRefuses is the rotation policy: exactly one match is
// an answer, zero and "more than one" are both refusals. Nothing in this package
// picks a key for the caller.
func TestByKidIsUnambiguousOrRefuses(t *testing.T) {
	t.Parallel()
	current := parseFixture(t, ecPublicDoc).WithKid("current")
	previous := parseFixture(t, okpPublicDoc).WithKid("previous")
	//: a rotation window where the publisher reused one id across two keys —
	//: legal under RFC 7517 §4.5, which only SHOULD-s distinct kids.
	shadowA := parseFixture(t, ecPublicDoc).WithKid("shared")
	shadowB := parseFixture(t, okpPublicDoc).WithKid("shared")
	set := jwk.NewSet(current, previous, shadowA, shadowB)
	tests := []struct {
		name     string
		kid      string
		wantKID  string
		wantCode errs.Code
	}{
		{"a unique kid resolves", "current", "current", 0},
		{"the other unique kid resolves", "previous", "previous", 0},
		{"an unknown kid is KeyNotFound", "absent", "", jwk.CodeJWKKeyNotFound},
		{"an empty kid resolves nothing", "", "", jwk.CodeJWKKeyNotFound},
		{"a duplicated kid is refused, not guessed", "shared", "", jwk.CodeJWKAmbiguousKid},
	}
	runCase := func(t *testing.T, c struct {
		name     string
		kid      string
		wantKID  string
		wantCode errs.Code
	},
	) {
		t.Helper()
		key, err := set.ByKid(c.kid)
		//: the success rows must return exactly the named key.
		if c.wantCode == 0 {
			if err != nil || key.Kid() != c.wantKID {
				t.Errorf("ByKid(%q)=(%v,%v) want kid %q", c.kid, key, err, c.wantKID)
			}
			return
		}
		//: the refusal rows must return the zero Key AND the typed code — a
		//: caller must not be able to mistake a refusal for a match.
		if !key.IsZero() || !errs.HasCode(err, c.wantCode) {
			t.Errorf("ByKid(%q)=(%v,%v) want zero key + code %v", c.kid, key, err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAllByKidIsTheRotationPath checks the escape hatch ByKid points at: every
// candidate, in document order, so the caller can try each in turn.
func TestAllByKidIsTheRotationPath(t *testing.T) {
	t.Parallel()
	first := parseFixture(t, ecPublicDoc).WithKid("shared").WithAlg("ES256")
	second := parseFixture(t, okpPublicDoc).WithKid("shared").WithAlg("EdDSA")
	set := jwk.NewSet(first, second, parseFixture(t, octDoc))
	tests := []struct {
		name     string
		kid      string
		wantAlgs []string
	}{
		{"both candidates, in document order", "shared", []string{"ES256", "EdDSA"}},
		{"a kid nobody carries yields nothing", "absent", nil},
		{"an empty kid yields nothing, not the keyless members", "", nil},
	}
	runCase := func(t *testing.T, c struct {
		name     string
		kid      string
		wantAlgs []string
	},
	) {
		t.Helper()
		got := set.AllByKid(c.kid)
		if len(got) != len(c.wantAlgs) {
			t.Fatalf("AllByKid(%q) returned %d keys want %d", c.kid, len(got), len(c.wantAlgs))
		}
		for i, key := range got {
			//: document order is the caller's policy input; it must survive.
			if key.Alg() != c.wantAlgs[i] {
				t.Errorf("candidate %d alg=%q want %q", i, key.Alg(), c.wantAlgs[i])
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

// TestSetSerialisationInheritsTheKeyPolicy: a set has no separate opinion about
// private material — it just cannot serialise more than its members allow, and
// it refuses whole rather than dropping a member.
func TestSetSerialisationInheritsTheKeyPolicy(t *testing.T) {
	t.Parallel()
	publicSet := jwk.NewSet(parseFixture(t, ecPublicDoc), parseFixture(t, okpPublicDoc))
	privateSet := jwk.NewSet(parseFixture(t, ecPrivateDoc), parseFixture(t, okpPrivateDoc))
	mixedSet := jwk.NewSet(parseFixture(t, ecPublicDoc), parseFixture(t, octDoc))
	tests := []struct {
		name     string
		invoke   func() ([]byte, error)
		wantCode errs.Code
		reject   string
	}{
		{"a public set publishes", publicSet.MarshalPublic, 0, `"d":`},
		{"a private set publishes its public half only", privateSet.MarshalPublic, 0, `"d":`},
		{"json.Marshal of a private set is public too", func() ([]byte, error) { return json.Marshal(privateSet) }, 0, `"d":`},
		{"a symmetric member blocks the public path", mixedSet.MarshalPublic, jwk.CodeJWKNoPublicForm, ""},
		{"json.Marshal refuses it as well", func() ([]byte, error) { return json.Marshal(mixedSet) }, jwk.CodeJWKNoPublicForm, ""},
		{"a public member blocks the private path", publicSet.MarshalPrivate, jwk.CodeJWKNoPrivateMaterial, ""},
	}
	runCase := func(t *testing.T, c struct {
		name     string
		invoke   func() ([]byte, error)
		wantCode errs.Code
		reject   string
	},
	) {
		t.Helper()
		out, err := c.invoke()
		//: the refusal rows check the typed code and that nothing came back.
		if c.wantCode != 0 {
			if out != nil || !errs.HasCode(err, c.wantCode) {
				t.Errorf("%s=(%s,%v) want code %v", c.name, out, err, c.wantCode)
			}
			return
		}
		document := string(out)
		//: the success rows check the document is complete and secret-free.
		if err != nil || !strings.Contains(document, `"keys":[`) {
			t.Fatalf("%s=(%s,%v)", c.name, out, err)
		}
		if strings.Contains(document, c.reject) {
			t.Errorf("%s leaked %s: %s", c.name, c.reject, out)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestSetRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		doc     string
		private bool
	}{
		{"public set", setDoc(ecPublicDoc, okpPublicDoc), false},
		{"private set", setDoc(ecPrivateDoc, okpPrivateDoc, octDoc), true},
		{"empty set", `{"keys":[]}`, false},
	}
	runCase := func(t *testing.T, c struct {
		name    string
		doc     string
		private bool
	},
	) {
		t.Helper()
		set, err := jwk.ParseSet([]byte(c.doc))
		if err != nil {
			t.Fatalf("ParseSet: %v", err)
		}
		render := set.MarshalPublic
		//: a set holding secrets can only round-trip through the private path.
		if c.private {
			render = set.MarshalPrivate
		}
		out, merr := render()
		if merr != nil {
			t.Fatalf("marshal: %v", merr)
		}
		back, berr := jwk.ParseSet(out)
		if berr != nil || back.Len() != set.Len() {
			t.Fatalf("re-parse=(%d,%v) want %d keys", back.Len(), berr, set.Len())
		}
		for i, key := range back.Keys() {
			//: every member must survive byte-for-byte, secret included.
			if !key.Equal(set.Keys()[i]) {
				t.Errorf("member %d changed across the round trip", i)
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

// TestParseSetIsTheOnlyDecodeEntryPoint mirrors the KeyValue case: Set marshals
// through json.Marshaler (safe default) but does not implement json.Unmarshaler,
// so a reflective decode yields the zero Set rather than a half-validated one.
func TestParseSetIsTheOnlyDecodeEntryPoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
		want int
	}{
		{"a one-key set", setDoc(okpPublicDoc), 1},
		{"a three-key set", setDoc(ecPublicDoc, okpPublicDoc, octDoc), 3},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
		want int
	},
	) {
		t.Helper()
		//: the realistic shape: a Set sitting in somebody's payload struct.
		var envelope struct {
			Keys jwk.Set `json:"jwks"`
		}
		//: no Unmarshaler, no exported fields — nothing lands in the value.
		if err := json.Unmarshal([]byte(`{"jwks":`+c.doc+`}`), &envelope); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		//: an empty Set is inert, never a partially trusted key store.
		if envelope.Keys.Len() != 0 {
			t.Fatalf("reflective decode populated a Set: %v", envelope.Keys)
		}
		//: ParseSet is the path that actually produces the members.
		parsed, perr := jwk.ParseSet([]byte(c.doc))
		if perr != nil || parsed.Len() != c.want {
			t.Errorf("ParseSet=(%d,%v) want %d keys", parsed.Len(), perr, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestSetKeysIsACopy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"mutating the returned slice cannot reach the set"},
	}
	runCase := func(t *testing.T, _ struct{ name string }) {
		t.Helper()
		set := jwk.NewSet(parseFixture(t, ecPublicDoc), parseFixture(t, okpPublicDoc))
		got := set.Keys()
		got[0] = jwk.KeyValue{}
		//: the Set must still hold its original member.
		if set.Keys()[0].IsZero() {
			t.Errorf("Keys() aliased the Set's own slice")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
