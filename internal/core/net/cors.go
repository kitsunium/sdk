package net

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// corsMaxAge is the longest lifetime a preflight may be given.
	corsMaxAge time.Duration = 24 * time.Hour
	// corsWildcard is the one non-origin Origins may hold.
	corsWildcard string = "*"
	// corsNullOrigin is the literal a sandboxed page or a local file sends.
	corsNullOrigin string = "null"
	// fieldListSeparator joins a field's values, as RFC 9110 §5.6.1 spells a list.
	fieldListSeparator string = ", "
	// fieldEnd closes a field line.
	fieldEnd string = "\r\n"
	// nameValueSeparator closes a field name.
	nameValueSeparator string = ": "
)

// The CORS response fields.
const (
	corsVary             string = "Vary"
	corsAllowOrigin      string = "Access-Control-Allow-Origin"
	corsAllowMethods     string = "Access-Control-Allow-Methods"
	corsAllowHeaders     string = "Access-Control-Allow-Headers"
	corsExposeHeaders    string = "Access-Control-Expose-Headers"
	corsAllowCredentials string = "Access-Control-Allow-Credentials"
	corsMaxAgeField      string = "Access-Control-Max-Age"
)

// CORSValue is a cross-origin resource sharing policy: which origins a browser
// may read answers from, and with what. Its zero value is refused: naming no
// origin would let nothing through while looking configured (ADR 0031). An
// origin is compared byte for byte and never echoed unless listed; an origin
// not listed receives no CORS field and never a 403, since the browser is what
// enforces the answer.
type CORSValue struct {
	// Origins are the exact origins allowed, as a browser serializes them:
	// scheme://host[:port], in lower case, with no path. "*" allows any origin,
	// alone in the list and without Credentials. "null" is refused: it is what a
	// sandboxed page and a local file both send.
	Origins []string
	// Methods are the methods a preflight may ask for; empty selects the
	// CORS-safelisted GET, HEAD and POST, never every method.
	Methods []string
	// Headers are the request fields a preflight may ask for; empty allows only
	// the CORS-safelisted fields.
	Headers []string
	// ExposeHeaders are the response fields a page may read.
	ExposeHeaders []string
	// Credentials lets a page send cookies and read the answer. It is refused
	// with "*": that combination publishes every user's answers to every site.
	Credentials bool
	// MaxAge is how long a browser may keep a preflight's answer, clamped to 24
	// hours; zero writes no Access-Control-Max-Age, negative is refused.
	MaxAge DurationValue
}

// CORSFieldValue is one response field of a CORS answer, for an engine that
// sets its fields by name.
type CORSFieldValue struct {
	// Name is the field name.
	Name string
	// Value is the field value.
	Value string
}

// CORSAnswerValue is what a CORSPolicy gives one response: the same fields twice,
// built once and shared read only.
type CORSAnswerValue struct {
	// Lines are the field lines, each "Name: value\r\n", for the http1 codec.
	Lines []byte
	// Fields are the fields by name, for net/http's Header.
	Fields []CORSFieldValue
}

// corsSafeMethods are the CORS-safelisted methods, a policy's default.
var corsSafeMethods = []string{"GET", "HEAD", "POST"}

// corsOrigin is what one origin is answered, built once.
type corsOrigin struct {
	// actual answers a request that is not a preflight.
	actual CORSAnswerValue
	// preflight answers a preflight.
	preflight CORSAnswerValue
}

// CORSPolicy is a CORSValue checked and built: what each origin is answered, ready
// before the first request. It is safe for concurrent use and allocates nothing
// per answer.
type CORSPolicy struct {
	// origins finds an exact origin's answers; a lookup by a []byte key converts
	// nothing.
	origins map[string]*corsOrigin
	// any answers every origin when the policy is "*"; nil otherwise.
	any *corsOrigin
	// methods are the methods a preflight may ask for.
	methods map[string]struct{}
	// vary answers an origin not listed: Vary: Origin alone.
	vary CORSAnswerValue
}

// NewCORSPolicy checks cfg and builds what each origin is answered, refusing
// with CORSMisconfigured, the "option" field named, whatever it cannot honour.
func NewCORSPolicy(cfg CORSValue) (*CORSPolicy, error) {
	methods := cfg.Methods
	//: empty is the safe default, the CORS-safelisted methods, never every method.
	if len(methods) == 0 {
		methods = corsSafeMethods
	}
	//: each of its checks names what it refused.
	if err := cfg.check(methods); err != nil {
		//: refused.
		return nil, err
	}
	p := &CORSPolicy{
		origins: make(map[string]*corsOrigin, len(cfg.Origins)),
		methods: make(map[string]struct{}, len(methods)),
		vary:    newCORSAnswer([]CORSFieldValue{{Name: corsVary, Value: "Origin"}}),
	}
	//: the methods the preflight's question is compared against.
	for _, m := range methods {
		p.methods[m] = struct{}{}
	}
	//: a wildcard answers one way, whatever the origin.
	if cfg.Origins[0] == corsWildcard {
		p.any = cfg.answers(corsWildcard, methods)
		//: built.
		return p, nil
	}
	//: each exact origin is echoed from the list, never from the request.
	for _, origin := range cfg.Origins {
		p.origins[origin] = cfg.answers(origin, methods)
	}
	//: built.
	return p, nil
}

// answers builds what origin is answered.
func (c *CORSValue) answers(origin string, methods []string) *corsOrigin {
	common := []CORSFieldValue{{Name: corsVary, Value: "Origin"}, {Name: corsAllowOrigin, Value: origin}}
	//: only an exact origin can carry them, the wildcard having been refused with them.
	if c.Credentials {
		common = append(common, CORSFieldValue{Name: corsAllowCredentials, Value: "true"})
	}
	actual := common
	//: what a page may read of the answer.
	if len(c.ExposeHeaders) > 0 {
		actual = append(slices.Clone(common),
			CORSFieldValue{Name: corsExposeHeaders, Value: strings.Join(c.ExposeHeaders, fieldListSeparator)})
	}
	preflight := append(slices.Clone(common),
		CORSFieldValue{Name: corsAllowMethods, Value: strings.Join(methods, fieldListSeparator)})
	//: no list, no field: only the CORS-safelisted request fields are allowed.
	if len(c.Headers) > 0 {
		preflight = append(preflight, CORSFieldValue{Name: corsAllowHeaders, Value: strings.Join(c.Headers, fieldListSeparator)})
	}
	//: zero writes nothing, and the browser keeps its own short default.
	if c.MaxAge > 0 {
		seconds := int64(min(c.MaxAge.Duration(), corsMaxAge) / time.Second)
		preflight = append(preflight, CORSFieldValue{Name: corsMaxAgeField, Value: strconv.FormatInt(seconds, decimalBase)})
	}
	//: both answers, each one's bytes built once.
	return &corsOrigin{actual: newCORSAnswer(actual), preflight: newCORSAnswer(preflight)}
}

// newCORSAnswer spells fields as lines once, keeping both forms.
func newCORSAnswer(fields []CORSFieldValue) CORSAnswerValue {
	var b strings.Builder
	//: each field, in order.
	for _, f := range fields {
		b.WriteString(f.Name)
		b.WriteString(nameValueSeparator)
		b.WriteString(f.Value)
		b.WriteString(fieldEnd)
	}
	//: the two forms of the same fields.
	return CORSAnswerValue{Lines: []byte(b.String()), Fields: fields}
}

// check refuses what the policy cannot honour.
func (c *CORSValue) check(methods []string) error {
	//: every kind of value, the first failure named.
	switch {
	case !c.originsValid():
		return corsRefusal("origins")
	case !allTokens(methods):
		return corsRefusal("methods")
	case !allTokens(c.Headers):
		return corsRefusal("headers")
	case !allTokens(c.ExposeHeaders):
		return corsRefusal("expose_headers")
	case c.MaxAge < 0:
		return corsRefusal("max_age")
	}
	//: all honourable.
	return nil
}

// originsValid reports whether Origins is a list the policy can compare: one
// exact origin after another, each once, or "*" alone without Credentials.
func (c *CORSValue) originsValid() bool {
	//: no origin at all is a configured nothing (ADR 0031).
	if len(c.Origins) == 0 {
		//: refused.
		return false
	}
	//: the wildcard: alone, and never with credentials.
	if c.Origins[0] == corsWildcard {
		//: "*" with Credentials publishes every user's answers to every site.
		return len(c.Origins) == 1 && !c.Credentials
	}
	seen := make(map[string]struct{}, len(c.Origins))
	//: each origin, each once.
	for _, origin := range c.Origins {
		_, again := seen[origin]
		//: a repeat, the wildcard among others, or not an origin.
		if again || !validOrigin(origin) {
			//: refused.
			return false
		}
		seen[origin] = struct{}{}
	}
	//: all exact and different.
	return true
}

// validOrigin reports whether s is one serialized origin (RFC 6454 §6.2):
// a lower-case scheme, "://", a lower-case host, an optional port, and nothing
// else. Anything looser could never equal what a browser sends, and "null" is
// what an attacker's sandboxed page sends.
func validOrigin(s string) bool {
	//: a literal no site owns.
	if s == corsNullOrigin {
		//: refused.
		return false
	}
	scheme, rest, ok := strings.Cut(s, "://")
	//: both halves are needed.
	if !ok || scheme == "" || rest == "" {
		//: refused.
		return false
	}
	//: each byte of both halves; a path, a query or a control fails here.
	return originText(scheme, false) && originText(rest, true)
}

// originText reports whether s holds only a lower-case origin's bytes: a letter,
// a digit, '+', '-' or '.', and, in the authority, ':' and the brackets of an
// IPv6 literal.
func originText(s string, authority bool) bool {
	//: every byte.
	for i := range len(s) {
		//: one byte outside the set.
		if !originByte(s[i], authority) {
			//: refused.
			return false
		}
	}
	//: every byte is one an origin carries.
	return true
}

// originByte reports whether c is one of an origin's bytes: the letters a to z,
// the digits, '+', '-' and '.', and, in the authority, ':', '[' and ']'.
func originByte(c byte, authority bool) bool {
	//: a letter or a digit.
	if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
		//: both halves take them.
		return true
	}
	//: the punctuation each half takes.
	if authority {
		//: the port and a bracketed IPv6 literal.
		return strings.IndexByte("-+.:[]", c) >= 0
	}
	//: a scheme's punctuation.
	return strings.IndexByte("-+.", c) >= 0
}

// allTokens reports whether every name is an RFC 9110 token.
func allTokens(names []string) bool {
	//: every name.
	for _, name := range names {
		//: one that a field line could not carry.
		if !isToken(name) {
			//: refused.
			return false
		}
	}
	//: all tokens.
	return true
}

// isToken reports whether s is a non-empty RFC 9110 token.
func isToken(s string) bool {
	//: an empty name is no token.
	if s == "" {
		//: refused.
		return false
	}
	//: every byte a tchar.
	for i := range len(s) {
		//: one byte outside tchar.
		if !isTchar(s[i]) {
			//: refused.
			return false
		}
	}
	//: a token.
	return true
}

// isTchar reports whether c is an RFC 9110 tchar.
func isTchar(c byte) bool {
	//: a letter, a digit or one of the fifteen punctuation marks.
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// corsRefusal names what the policy refused.
func corsRefusal(option string) error {
	//: once, at construction.
	return errs.Wrap(CORSMisconfigured, errs.WrapParams{}, errs.String("option", option))
}

// Actual is the answer to a request that is not a preflight, from origin:
// Vary: Origin always, and the CORS fields when origin is listed. An empty
// origin is a request carrying none.
func (p *CORSPolicy) Actual(origin []byte) CORSAnswerValue {
	e := p.lookup(origin)
	//: not listed: the browser refuses, and the answer still varies on Origin.
	if e == nil {
		//: Vary alone.
		return p.vary
	}
	//: the origin's own.
	return e.actual
}

// Preflight is the answer to a preflight from origin asking for method: the
// CORS fields when origin and method are both allowed, Vary: Origin alone
// otherwise, so the browser refuses it. It never answers a 403.
func (p *CORSPolicy) Preflight(origin, method []byte) CORSAnswerValue {
	e := p.lookup(origin)
	_, allowed := p.methods[string(method)]
	//: either one absent is the same answer.
	if e == nil || !allowed {
		//: Vary alone.
		return p.vary
	}
	//: the origin's own.
	return e.preflight
}

// lookup returns origin's answers, nil when it is not allowed.
func (p *CORSPolicy) lookup(origin []byte) *corsOrigin {
	//: no Origin field: nothing to answer it with.
	if len(origin) == 0 {
		//: none.
		return nil
	}
	//: the one wildcard entry.
	if p.any != nil {
		//: for every origin.
		return p.any
	}
	//: the compiler converts this key without copying it; absent is nil.
	return p.origins[string(origin)]
}
