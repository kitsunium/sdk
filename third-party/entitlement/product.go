// Package entitlement — the product: which vendor's roster this binary trusts,
// and the names its artefacts carry.
package entitlement

import (
	"errors"
	"net/url"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// unusableHost is the single bucket every origin that names no fetchable host
// falls into. It is one shared value, not the URL itself: returning the URL made
// two DIFFERENT malformed entries look like two different hosts and satisfy the
// redundancy requirement, while neither could ever be fetched.
const unusableHost string = "\x00unusable"

// minRedundantHosts is the number of DISTINCT hosts a product's origin list
// must span to count as redundant. Two branches on one host share that host's
// outage, so a list where every entry resolves to the same name looks redundant
// while being a single point of failure.
const minRedundantHosts int = 2

// ProductValue names the product whose entitlement is being verified, and where
// its roster is published.
//
// It is the whole of what the source implementation kept as package constants:
// the publication origins, the cache directory name, the OIDC audience, and the
// enrolment issue URL. Every one is a property of one vendor's distribution
// rather than of the verification mechanism, and together they are the only
// reason a correct implementation served exactly one binary.
//
// The zero value is usable and inert in the safe direction: no origins means
// nothing to fetch, so Verify refuses with RosterUnreachable rather than
// silently trusting anything.
type ProductValue struct {
	// Name is the product's binary name. It scopes the cache directory and
	// labels generated key material.
	Name string
	// Origins are the roster publication locations, tried in order. The first
	// that answers with an authentic bundle wins.
	Origins []OriginValue
	// CIAudience is the OIDC audience a CI provenance token must carry. It must
	// be unique to this product: a token minted for one audience must not
	// satisfy another's gate.
	CIAudience string
	// EnrolURL is where a new subject is directed to request enrolment. Empty
	// means the product offers no self-service path.
	EnrolURL string
}

// cacheDir returns the directory name this product's cache is scoped under.
//
// The receiver is a pointer on every method here because ProductValue is 72
// bytes — four strings and a slice header — and copying all of it to read one
// field is what KTN-VAR-BIGSTRUCT exists to catch. Hold the product in a
// variable and call on that.
// A product with no name falls back to the package name rather than writing to
// the cache root itself.
func (p *ProductValue) cacheDir() string {
	//: a nil product is a Service built without one — the same fallback, not a
	//: panic. Every method here tolerates it for that reason.
	if p == nil || p.Name == "" {
		//: the documented fallback.
		return "entitlement"
	}

	//: the product's own directory.
	return p.Name
}

// audience returns the OIDC audience for this product's CI seats, falling back
// to a package-scoped value that no other product would mint a token for.
func (p *ProductValue) audience() string {
	//: an unset audience — or no product at all — must not become the empty
	//: string, which some issuers treat as "any".
	if p == nil || p.CIAudience == "" {
		//: a value specific enough not to collide by accident.
		return "kitsunium-sdk-entitlement"
	}

	//: the product's own audience.
	return p.CIAudience
}

// Validate reports the ways a product's origin list would fail it when it
// matters, which is the one moment nobody is watching: the day the roster
// cannot be fetched.
//
// The source implementation asserted these as a test over its own shipped
// constants. Moving the origins to the caller would have moved the property out
// of reach with them, so the assertions become a method any consumer can call —
// at construction, or in its own suite.
//
// Four things are checked, and the last is the one that is easy to get wrong:
//
//   - no empty Name or BundleURL, since an origin that cannot be named cannot
//     be reported when it fails;
//   - no duplicate name;
//   - no duplicate URL, because two entries at one URL are one origin listed
//     twice rather than a fallback;
//   - at least two DISTINCT hosts. Independent publication paths within one
//     repository do count — that is what survives a ruleset refusing the
//     signing bot's push to the default branch — but they must not all resolve
//     to the same host.
//
// It returns a joined error naming every failure rather than the first, because
// a caller fixing an origin list wants the whole list.
func (p *ProductValue) Validate() error {
	//: a nil product has no origins, which is the first thing Validate refuses.
	if p == nil {
		//: report it as the same misconfiguration rather than panicking.
		return misconfigured("no product was supplied")
	}

	seen := newOriginScan(len(p.Origins))
	//: Walk every origin: each one can fail independently.
	for _, origin := range p.Origins {
		seen.admit(origin)
	}

	//: Redundancy that shares a host is not redundancy.
	if len(seen.hosts) < minRedundantHosts {
		seen.problems = append(seen.problems,
			misconfigured("origins span fewer than two distinct hosts, so one outage takes them all down"))
	}

	//: Join so a caller fixing a list sees every problem, not the first.
	return errors.Join(seen.problems...)
}

// misconfigured builds the one refusal every Validate failure carries, so the
// code, reason and public text are written once.
func misconfigured(detail string) error {
	//: one sentinel shape, one private detail per call site.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeProductInvalid,
		Reason:  "PRODUCT_INVALID",
		Public:  "the entitlement product is misconfigured",
		Private: "third-party/entitlement: " + detail,
	})
}

// originScan is Validate's accumulator: what it has seen so far, and what it has
// to say about it.
type originScan struct {
	names    map[string]bool
	urls     map[string]string
	hosts    map[string]bool
	problems []error
}

// newOriginScan pre-sizes an accumulator for a list of n origins.
func newOriginScan(n int) *originScan {
	//: three small maps, sized once.
	return &originScan{
		names: make(map[string]bool, n),
		urls:  make(map[string]string, n),
		hosts: make(map[string]bool, n),
	}
}

// admit folds one origin into the scan, recording every way it fails.
func (s *originScan) admit(origin OriginValue) {
	//: An origin that cannot be named cannot be reported when it fails.
	if origin.Name == "" || origin.BundleURL == "" {
		s.problems = append(s.problems, misconfigured("an origin has an empty Name or BundleURL"))

		return
	}
	//: A repeated name makes two failures indistinguishable in a log.
	if s.names[origin.Name] {
		s.problems = append(s.problems, misconfigured("origin name used twice: "+origin.Name))
	}
	s.names[origin.Name] = true
	//: Two entries at one URL are one origin listed twice, not a fallback.
	if other, dup := s.urls[origin.BundleURL]; dup {
		s.problems = append(s.problems,
			misconfigured("origins "+other+" and "+origin.Name+" publish at the same URL"))
	}
	s.urls[origin.BundleURL] = origin.Name

	host := hostOf(origin.BundleURL)
	//: An entry naming no fetchable host contributes NOTHING to the count.
	//: Counting the shared bucket would let one real origin plus one
	//: unfetchable entry add up to the two hosts this requires.
	if host == unusableHost {
		s.problems = append(s.problems,
			misconfigured("origin "+origin.Name+" names no fetchable http(s) host"))

		return
	}
	s.hosts[host] = true
}

// hostOf extracts the DNS host an origin resolves to, for the redundancy check.
//
// Two normalisations matter, and both were defects before they were rules:
//
//   - the PORT is dropped. https://h/a and https://h:8443/a are one machine and
//     one outage; counting them as two hosts is how a list looks redundant
//     while being a single point of failure.
//   - the case is folded, because DNS is case-insensitive and Example.com and
//     example.com are the same name.
//
// Anything that does not parse, names no host, or is not http(s) collapses into
// unusableHost, which Validate then refuses outright and never counts — so
// neither a pile of unfetchable entries nor one real origin beside one
// unfetchable entry can add up to redundancy.
func hostOf(bundleURL string) string {
	parsed, err := url.Parse(bundleURL)
	//: One refusal for the three ways an entry names no fetchable host: it
	//: does not parse, it carries no host (a relative URL), or its scheme is
	//: not one the fetch can use. All three land in the same shared bucket.
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		//: One shared bucket for everything unfetchable.
		return unusableHost
	}

	//: Hostname() already strips the port; fold the case DNS ignores.
	return strings.ToLower(parsed.Hostname())
}
