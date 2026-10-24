package token

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The differential oracle: the scanner against the legacy encoding/json
// reader (legacy_reader_test.go), over the same bytes, in four cases.
//
//  1. Both accept: the claims must be identical — every registered claim and
//     every private claim's raw bytes — and VerifyInto's buffer must hold the
//     same values, decoded.
//  2. The legacy reader accepts and the scanner refuses: the input must carry
//     one of the LISTED divergence classes (divergenceClasses), each a
//     deliberate refusal; anything else fails.
//  3. The legacy reader refuses and the scanner accepts: ALWAYS a failure.
//  4. Both refuse: the codes must be equal, or differ as the written
//     precedence (precedenceAllows) permits.

// divergence is one listed class of input the scanner refuses and the legacy
// reader accepted.
type divergence string

const (
	// escapedName: a top-level member name carrying a backslash. The legacy
	// reader decoded "exp" to "exp"; the scanner refuses the second
	// spelling outright (Malformed).
	escapedName divergence = "escaped-member-name"
	// loneSurrogate: a \uD800-\uDFFF escape that is not half of a pair. The
	// legacy reader replaced it with U+FFFD, so "\ud800" and "\udbff" were one
	// string; the scanner refuses it (Malformed).
	loneSurrogate divergence = "lone-surrogate"
	// nullAudienceMember: a null inside an "aud" array, which the legacy reader
	// decoded as "" (Malformed).
	nullAudienceMember divergence = "null-aud-member"
	// headerMemberCap: a JOSE header with more members than maxIndexedMembers;
	// the legacy reader had no bound (TooLarge).
	headerMemberCap divergence = "header-member-cap"
)

// divergenceClasses names every listed class raw carries, judged without the
// scanner: a byte walk for the escapes, the legacy decoder for the rest.
func divergenceClasses(raw []byte, header bool) []divergence {
	var classes []divergence
	//: a backslash in a top-level name, located through encoding/json's own
	//: offsets so the scanner under test is not consulted.
	if topLevelNameEscaped(raw) {
		classes = append(classes, escapedName)
	}
	//: an unpaired surrogate escape anywhere.
	if hasLoneSurrogate(raw) {
		classes = append(classes, loneSurrogate)
	}
	var members map[string]json.RawMessage
	//: the remaining classes read the legacy decoder's own view.
	if json.Unmarshal(raw, &members) == nil {
		//: a null member of the audience array.
		var audience []any
		if json.Unmarshal(members[coretoken.ClaimAudience], &audience) == nil && slices.Contains(audience, nil) {
			classes = append(classes, nullAudienceMember)
		}
		//: the header's member count.
		if header && len(members) > maxIndexedMembers {
			classes = append(classes, headerMemberCap)
		}
	}
	//: possibly none.
	return classes
}

// topLevelNameEscaped reports whether a top-level member name of raw is
// spelled with a backslash.
func topLevelNameEscaped(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	//: the opening brace.
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		//: not an object: no member name.
		return false
	}
	//: name, value, name, value…
	for dec.More() {
		before := dec.InputOffset()
		//: the name token.
		if _, err := dec.Token(); err != nil {
			//: unreadable from here.
			return false
		}
		//: between the previous token and this one there is only a separator,
		//: whitespace and the name itself — a backslash is in the name.
		if bytes.IndexByte(raw[before:dec.InputOffset()], '\\') >= 0 {
			//: escaped.
			return true
		}
		var skip json.RawMessage
		//: the value.
		if dec.Decode(&skip) != nil {
			//: unreadable from here.
			return false
		}
	}
	//: every name read plainly.
	return false
}

// hasLoneSurrogate reports whether raw carries a surrogate escape that is not
// a high half followed at once by an escaped low half.
func hasLoneSurrogate(raw []byte) bool {
	//: every backslash, wherever it is; an escaped backslash is skipped whole.
	for i := 0; i < len(raw)-1; i++ {
		//: not an escape.
		if raw[i] != '\\' {
			continue
		}
		//: anything but \u is two bytes.
		if raw[i+1] != 'u' {
			i++
			continue
		}
		unit, ok := hexUnit(raw[i:])
		//: a malformed escape is another refusal.
		if !ok {
			continue
		}
		//: a low half on its own.
		if unit >= 0xDC00 && unit <= 0xDFFF {
			//: lone.
			return true
		}
		//: a high half needs its low half.
		if unit >= 0xD800 && unit <= 0xDBFF {
			low, lok := hexUnit(raw[i+unicodeEscapeLen:])
			//: unpaired.
			if !lok || low < 0xDC00 || low > 0xDFFF {
				//: lone.
				return true
			}
			i += unicodeEscapeLen
		}
		i += unicodeEscapeLen - 1
	}
	//: every surrogate paired.
	return false
}

// precedenceAllows reports whether the scanner may refuse with newCode where
// the legacy reader refused with oldCode. The written precedence:
//
//   - equal codes always;
//   - Malformed for anything, when the input carries a listed class — the
//     scanner refuses the class where it is met, the legacy reader went on to
//     refuse something later;
//   - TooLarge for DuplicateMember or ClaimNameInvalid, when the object has
//     more than maxClaimMembers members — duplicates are not looked for past
//     the cap, and the legacy reader's private-claim order was its map's;
//   - ClaimNameInvalid and TooLarge for each other, likewise.
func precedenceAllows(oldCode, newCode errs.Code, classes []divergence, members int) bool {
	//: the common case.
	if oldCode == newCode {
		//: same verdict.
		return true
	}
	//: a listed class is refused where it stands.
	if len(classes) > 0 && newCode == coretoken.CodeMalformed {
		//: allowed.
		return true
	}
	capped := members > maxClaimMembers
	//: the cap bounds the duplicate search.
	if capped && newCode == coretoken.CodeTooLarge &&
		(oldCode == coretoken.CodeDuplicateMember || oldCode == coretoken.CodeClaimNameInvalid) {
		//: allowed.
		return true
	}
	//: the legacy private-claim walk ran in map order.
	pair := (oldCode == coretoken.CodeTooLarge && newCode == coretoken.CodeClaimNameInvalid) ||
		(oldCode == coretoken.CodeClaimNameInvalid && newCode == coretoken.CodeTooLarge)
	//: allowed only as that pair.
	return pair
}

// memberCount counts raw's top-level members the legacy way, -1 when it is no
// object.
func memberCount(raw []byte) int {
	dec := json.NewDecoder(bytes.NewReader(raw))
	//: the opening brace.
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		//: no object.
		return -1
	}
	count := 0
	//: name, value…
	for dec.More() {
		//: the name.
		if _, err := dec.Token(); err != nil {
			//: as far as it reads.
			return count
		}
		var skip json.RawMessage
		//: the value.
		if dec.Decode(&skip) != nil {
			//: as far as it reads.
			return count
		}
		count++
	}
	//: every member.
	return count
}

// oracleRig is one verification policy both readers are judged under.
type oracleRig struct {
	// policy is the shared verification policy.
	policy policyValue
}

// newOracleRig builds a policy that accepts a token without exp, at a fixed
// instant, so the oracle exercises the readers and the policy together.
func newOracleRig(t testing.TB) oracleRig {
	t.Helper()
	policy, err := newPolicy(VerifierConfig{
		AllowMissingExpiry: true,
		Clock:              clock.NewManualClock(time.Unix(1_700_000_000, 0)),
	})
	//: the policy is valid by construction.
	if err != nil {
		t.Fatalf("newPolicy: %v", err)
	}
	//: ready.
	return oracleRig{policy: policy}
}

// judge runs the four-case oracle over one JOSE claims object.
func (r oracleRig) judge(t *testing.T, raw []byte) {
	t.Helper()
	oldClaims, oldErr := legacyDecodeClaims(raw, r.policy.maxClaimDepth, legacyJose{})
	//: the legacy pipeline judged the policy after decoding.
	if oldErr == nil {
		oldErr = r.policy.validateClaims(oldClaims)
	}
	newClaims, newErr := decodeClaims(raw, r.policy.maxClaimDepth, joseShape{})
	//: the same policy, through the same judgement.
	if newErr == nil {
		newErr = r.policy.validateClaims(newClaims)
	}
	r.compare(t, raw, false, oldClaims, oldErr, newClaims, newErr)
	r.judgeInto(t, raw, newClaims, newErr)
	r.judgePaseto(t, raw)
}

// judgePaseto runs the oracle over the same bytes read as a PASETO payload.
func (r oracleRig) judgePaseto(t *testing.T, raw []byte) {
	t.Helper()
	oldClaims, oldErr := legacyDecodeClaims(raw, r.policy.maxClaimDepth, legacyPaseto{})
	newClaims, newErr := decodeClaims(raw, r.policy.maxClaimDepth, pasetoShape{})
	r.compare(t, raw, false, oldClaims, oldErr, newClaims, newErr)
}

// compare applies the four cases to one pair of outcomes.
func (r oracleRig) compare(t *testing.T, raw []byte, header bool, oldClaims coretoken.ClaimsValue, oldErr error, newClaims coretoken.ClaimsValue, newErr error) {
	t.Helper()
	classes := divergenceClasses(raw, header)
	//: the four cases.
	switch {
	//: 1 — both accept: identical claims.
	case oldErr == nil && newErr == nil:
		//: field by field, private bytes included.
		if diff := claimsDiff(oldClaims, newClaims); diff != "" {
			t.Fatalf("both accept %q but the claims differ: %s", raw, diff)
		}
	//: 2 — legacy accepts, scanner refuses: a listed class only.
	case oldErr == nil:
		//: the class is the only licence.
		if len(classes) == 0 {
			t.Fatalf("the scanner refuses %q (%v) that the legacy reader accepts, outside every listed class", raw, newErr)
		}
	//: 3 — legacy refuses, scanner accepts: always a failure.
	case newErr == nil:
		t.Fatalf("the scanner ACCEPTS %q that the legacy reader refuses (%v)", raw, oldErr)
	//: 4 — both refuse: the written precedence.
	default:
		oldCode, newCode := codeOf(oldErr), codeOf(newErr)
		//: equal, or a permitted pair.
		if !precedenceAllows(oldCode, newCode, classes, memberCount(raw)) {
			t.Fatalf("both refuse %q, but the scanner says %v where the legacy reader says %v", raw, newErr, oldErr)
		}
	}
}

// judgeInto runs VerifyInto's claims half over raw and holds it to Verify's
// outcome: the same verdict, or ClaimsBufOverflow on an accepted token; and
// on acceptance the same values, decoded.
func (r oracleRig) judgeInto(t *testing.T, raw []byte, claims coretoken.ClaimsValue, verifyErr error) {
	t.Helper()
	buf, berr := coretoken.NewClaimsBuf("name", "role")
	//: the buffer is valid by construction.
	if berr != nil {
		t.Fatalf("NewClaimsBuf: %v", berr)
	}
	intoErr := r.policy.judgeInto(raw, buf)
	//: the four outcomes.
	switch {
	//: Verify refuses: VerifyInto refuses with the same code.
	case verifyErr != nil:
		//: one refusal set.
		if codeOf(intoErr) != codeOf(verifyErr) {
			t.Fatalf("Verify refuses %q with %v, VerifyInto with %v", raw, verifyErr, intoErr)
		}
	//: an overflow is the one refusal VerifyInto adds.
	case errs.HasCode(intoErr, coretoken.CodeClaimsBufOverflow):
		//: listed: the buffer's capacity.
	//: anything else VerifyInto refuses, Verify would have.
	case intoErr != nil:
		t.Fatalf("VerifyInto refuses %q (%v) that Verify accepts", raw, intoErr)
	//: both accept: the same values.
	default:
		//: field by field.
		if diff := bufDiff(claims, buf); diff != "" {
			t.Fatalf("Verify and VerifyInto accept %q with different claims: %s", raw, diff)
		}
	}
}

// claimsDiff names the first difference between two claim sets, "" if none.
func claimsDiff(a, b coretoken.ClaimsValue) string {
	//: the string claims.
	if a.Issuer() != b.Issuer() || a.Subject() != b.Subject() || a.ID() != b.ID() {
		//: named, values withheld from nothing — this is a test.
		return "iss/sub/jti: " + a.Issuer() + "|" + a.Subject() + "|" + a.ID() + " vs " + b.Issuer() + "|" + b.Subject() + "|" + b.ID()
	}
	//: the audience, member by member.
	if !slices.Equal(a.Audience(), b.Audience()) {
		//: named.
		return "aud: " + strings.Join(a.Audience(), ",") + " vs " + strings.Join(b.Audience(), ",")
	}
	//: the instants, compared as instants.
	if !a.Expiry().Equal(b.Expiry()) || !a.NotBefore().Equal(b.NotBefore()) || !a.IssuedAt().Equal(b.IssuedAt()) {
		//: named.
		return "exp/nbf/iat: " + a.Expiry().String() + " vs " + b.Expiry().String()
	}
	//: the private names.
	if !slices.Equal(a.PrivateNames(), b.PrivateNames()) {
		//: named.
		return "private names: " + strings.Join(a.PrivateNames(), ",") + " vs " + strings.Join(b.PrivateNames(), ",")
	}
	//: the private bytes, exactly.
	for _, name := range a.PrivateNames() {
		left, _ := a.PrivateRaw(name)
		right, _ := b.PrivateRaw(name)
		//: byte for byte.
		if !bytes.Equal(left, right) {
			//: named.
			return "private " + name + ": " + string(left) + " vs " + string(right)
		}
	}
	//: identical.
	return ""
}

// bufDiff names the first difference between a claim set and the buffer
// VerifyInto filled, "" if none.
func bufDiff(claims coretoken.ClaimsValue, buf *coretoken.ClaimsBuf) string {
	//: the three text claims; "" is stored as invalid, absent as absent.
	for _, field := range []struct {
		claim coretoken.TextClaim
		want  string
	}{{coretoken.TextIssuer, claims.Issuer()}, {coretoken.TextSubject, claims.Subject()}, {coretoken.TextID, claims.ID()}} {
		got, state := buf.Text(field.claim)
		//: a present claim matches; an empty one is absent or invalid.
		if string(got) != field.want || (field.want != "" && state != coretoken.ClaimOK) {
			//: named.
			return "text claim " + field.want + " vs " + string(got) + " (" + state.String() + ")"
		}
	}
	//: the instants.
	for _, field := range []struct {
		claim coretoken.TimeClaim
		want  time.Time
	}{{coretoken.TimeExpiry, claims.Expiry()}, {coretoken.TimeNotBefore, claims.NotBefore()}, {coretoken.TimeIssuedAt, claims.IssuedAt()}} {
		sec, nsec, state := buf.Unix(field.claim)
		//: absent on both sides, or the same instant.
		if field.want.IsZero() != (state == coretoken.ClaimAbsent) ||
			(!field.want.IsZero() && !time.Unix(sec, int64(nsec)).Equal(field.want)) {
			//: named.
			return "instant " + field.want.String()
		}
	}
	audience := claims.Audience()
	//: the audience, member by member.
	if buf.AudienceLen() != len(audience) {
		//: named.
		return "aud length"
	}
	//: in order.
	for i, want := range audience {
		got, _ := buf.Audience(i)
		//: decoded on both sides.
		if string(got) != want {
			//: named.
			return "aud member " + want
		}
	}
	//: the declared private claims, decoded.
	return privateDiff(claims, buf)
}

// privateDiff compares the two declared private claims.
func privateDiff(claims coretoken.ClaimsValue, buf *coretoken.ClaimsBuf) string {
	//: the two declared names.
	for _, name := range []string{"name", "role"} {
		raw, found := claims.PrivateRaw(name)
		got, state := buf.Private(name)
		//: the four states.
		switch {
		//: absent on both sides.
		case !found:
			//: must be absent.
			if state != coretoken.ClaimAbsent {
				//: named.
				return "private " + name + " should be absent"
			}
		//: not a string: wrong type.
		case len(raw) == 0 || raw[0] != '"':
			//: must be marked.
			if state != coretoken.ClaimWrongType {
				//: named.
				return "private " + name + " should be wrong_type"
			}
		//: a string: its decoding.
		default:
			var want string
			//: encoding/json decodes the legacy reader's raw bytes.
			if json.Unmarshal(raw, &want) != nil || string(got) != want ||
				(want == "") != (state == coretoken.ClaimInvalid) {
				//: named.
				return "private " + name + ": " + want + " vs " + string(got)
			}
		}
	}
	//: identical.
	return ""
}

// judgeHeader runs the oracle over one JOSE header object.
func judgeHeader(t *testing.T, raw []byte) {
	t.Helper()
	oldHeader, oldErr := legacyParseJOSEHeader(raw)
	newHeader, newErr := parseJOSEHeader(raw)
	classes := divergenceClasses(raw, true)
	//: the four cases, on headers.
	switch {
	//: 1 — identical members.
	case oldErr == nil && newErr == nil:
		//: field by field.
		if oldHeader != newHeader {
			t.Fatalf("both accept header %q but read %+v vs %+v", raw, oldHeader, newHeader)
		}
		policy := policyValue{requireType: "JWT"}
		view, _ := readJOSEHeader(raw)
		//: the two header judgements agree.
		if codeOf(policy.checkHeader(newHeader, coretoken.AlgorithmHS256)) !=
			codeOf(policy.checkHeaderView(&view, coretoken.AlgorithmHS256)) {
			t.Fatalf("checkHeader and checkHeaderView disagree on %q", raw)
		}
	//: 2 — a listed class only.
	case oldErr == nil:
		//: the class is the only licence.
		if len(classes) == 0 {
			t.Fatalf("the scanner refuses header %q (%v) the legacy reader accepts, outside every listed class", raw, newErr)
		}
	//: 3 — always a failure.
	case newErr == nil:
		t.Fatalf("the scanner ACCEPTS header %q the legacy reader refuses (%v)", raw, oldErr)
	//: 4 — the written precedence.
	default:
		//: equal, or a permitted pair.
		if !precedenceAllows(codeOf(oldErr), codeOf(newErr), classes, memberCount(raw)) {
			t.Fatalf("both refuse header %q: scanner %v, legacy %v", raw, newErr, oldErr)
		}
	}
}

// oracleSeeds is the seed corpus: every refusal the legacy reader made, the
// listed divergence classes, and the corners the plan names — null in aud,
// the empty name, the member cap, \ud800 against \udbff, escaped names.
func oracleSeeds() []string {
	seeds := []string{
		`{}`, ` { } `, `{"sub":"1","name":"alice"}`,
		`{"iss":"a","sub":"b","aud":"c","exp":1700000100,"nbf":1699999000,"iat":1699999900,"jti":"j"}`,
		`{"aud":["a","b"],"exp":1.5}`, `{"aud":[]}`, `{"aud":[null]}`, `{"aud":["a",null]}`, `{"aud":null}`,
		`{"aud":5}`, `{"aud":[1]}`, `{"aud":{}}`, `{"aud":["\u0061"]}`,
		`{"":1}`, `{"":1,"exp":"x"}`, `{"sub":null}`, `{"exp":null}`, `{"sub":5}`, `{"jti":true}`,
		`{"exp":-62135596800}`, `{"exp":1e400}`, `{"exp":1e-400}`, `{"exp":-0}`, `{"exp":1e11}`, `{"exp":100000000001}`,
		`{"exp":12345678901234567890}`, `{"exp":0.0000001}`, `{"exp":"1700000000"}`, `{"exp":01}`, `{"exp":1.}`,
		`{"exp":.5}`, `{"exp":+1}`, `{"exp":1e}`, `{"exp":-}`, `{"exp":0x10}`, `{"exp":Infinity}`, `{"exp":NaN}`,
		`{"sub":"\ud800"}`, `{"sub":"\udbff"}`, `{"sub":"\udc00"}`, `{"sub":"\ud800\udc00"}`, `{"sub":"\ud800\u0041"}`,
		`{"sub":"\uD83D\uDE00"}`, `{"name":"\ud800"}`, `{"n":{"\ud800":1}}`,
		`{"\u0065xp":1}`, `{"exp":1,"\u0065xp":2}`, `{"a\\b":1}`, `{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`,
		`{"a":1,}`, `{"a" 1}`, `{"a":1 "b":2}`, `[]`, `null`, `"x"`, `{"a":1} x`, `{"a":1}}`, `{`, `{"a`,
		`{"a":"\x"}`, "{\"a\":\"\x01\"}", "{\"a\":\"\xff\"}", `{"a":tru}`, `{"a":nul}`, `{"a":[1,]}`, `{"a":[,1]}`,
		`{"a":"\u12"}`, `{"a":"\/\b\f\n\r\t\"\\"}`, `{"name":"x\u0000y"}`, `{"name":123}`, `{"name":""}`,
		`{"role":["admin"]}`, `{"name":"é","role":"ü"}`, `{"a":{"b":{"c":{"d":1}}}}`,
		"{\"a\":1,\t\n\r \"b\":2}",
		`{"x":1e400}`, `{"x":[1e309]}`, `{"x":{"y":-1e400}}`, `{"x":1.7976931348623157e308}`,
		`{"x":1.7976931348623159e308}`, `{"x":17976931348623159e292}`, `{"x":0.0001e312}`, `{"x":0e999999}`,
		`{"x":1e-99999999999999999999}`, `{"x":1e99999999999999999999}`,
	}
	var at, over strings.Builder
	at.WriteString("{")
	over.WriteString("{")
	//: exactly at the member cap, and one past it with a duplicate there.
	for i := range maxClaimMembers {
		//: separated.
		if i > 0 {
			at.WriteString(",")
			over.WriteString(",")
		}
		at.WriteString(`"m` + itoaTest(i) + `":1`)
		over.WriteString(`"m` + itoaTest(i) + `":1`)
	}
	at.WriteString("}")
	over.WriteString(`,"m0":2}`)
	//: the cap, the cap plus a duplicate, and an oversized name.
	return append(seeds, at.String(), over.String(), `{"sub":"`+strings.Repeat("s", coretoken.ClaimsBufTextCap+1)+`"}`)
}

// itoaTest renders a small non-negative integer.
func itoaTest(n int) string {
	//: one digit.
	if n < decimalBase {
		//: the digit.
		return string(rune('0' + n))
	}
	//: recursively.
	return itoaTest(n/decimalBase) + itoaTest(n%decimalBase)
}

// TestScannerMatchesTheLegacyReader runs the four-case oracle over the seed
// corpus, claims and headers.
func TestScannerMatchesTheLegacyReader(t *testing.T) {
	t.Parallel()
	rig := newOracleRig(t)
	//: every seed, as claims and as a header.
	for _, seed := range oracleSeeds() {
		rig.judge(t, []byte(seed))
		judgeHeader(t, []byte(seed))
	}
}

// TestDivergenceClassesAreRefused pins each listed class: the legacy reader
// accepts it, and the scanner refuses it with the listed code.
func TestDivergenceClassesAreRefused(t *testing.T) {
	t.Parallel()
	var wide strings.Builder
	wide.WriteString(`{"alg":"HS256"`)
	//: one member past the header bound.
	for i := range maxIndexedMembers {
		wide.WriteString(`,"x` + itoaTest(i) + `":1`)
	}
	wide.WriteString("}")
	cases := []struct {
		raw    string
		header bool
		class  divergence
		code   errs.Code
	}{
		{`{"\u0065xp":1}`, false, escapedName, coretoken.CodeMalformed},
		{`{"sub":"\ud800"}`, false, loneSurrogate, coretoken.CodeMalformed},
		{`{"sub":"\udbff"}`, false, loneSurrogate, coretoken.CodeMalformed},
		{`{"aud":["a",null]}`, false, nullAudienceMember, coretoken.CodeMalformed},
		{wide.String(), true, headerMemberCap, coretoken.CodeTooLarge},
	}
	//: each class on its own.
	for _, tc := range cases {
		//: the class is the one the oracle would name.
		if !slices.Contains(divergenceClasses([]byte(tc.raw), tc.header), tc.class) {
			t.Fatalf("%s: %q is not classified", tc.class, tc.raw)
		}
		var oldErr, newErr error
		//: the header path or the claims path.
		if tc.header {
			_, oldErr = legacyParseJOSEHeader([]byte(tc.raw))
			_, newErr = parseJOSEHeader([]byte(tc.raw))
		} else {
			_, oldErr = legacyDecodeClaims([]byte(tc.raw), DefaultMaxClaimDepth, legacyJose{})
			_, newErr = decodeClaims([]byte(tc.raw), DefaultMaxClaimDepth, joseShape{})
		}
		//: the legacy reader accepted it.
		if oldErr != nil {
			t.Fatalf("%s: the legacy reader refuses %q (%v); the class is not a divergence", tc.class, tc.raw, oldErr)
		}
		//: the scanner refuses it, with the listed code.
		if !errs.HasCode(newErr, tc.code) {
			t.Fatalf("%s: scanner = %v, want code %v", tc.class, newErr, tc.code)
		}
	}
}

// FuzzClaimsReader is the differential fuzz target: arbitrary bytes, judged
// as a JOSE claims object, a PASETO one, VerifyInto's claims half, and a JOSE
// header, against the legacy reader. Run it with
// `go test -run=^$ -fuzz=FuzzClaimsReader ./security/token/`.
func FuzzClaimsReader(f *testing.F) {
	//: the seed corpus.
	for _, seed := range oracleSeeds() {
		f.Add([]byte(seed))
	}
	rig := newOracleRig(f)
	f.Fuzz(func(t *testing.T, raw []byte) {
		rig.judge(t, raw)
		judgeHeader(t, raw)
	})
}

// codeOf is the code of err, 0 when it carries none.
func codeOf(err error) errs.Code {
	code, _ := errs.CodeOf(err)
	//: the code alone.
	return code
}
