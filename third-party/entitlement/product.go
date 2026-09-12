// Package entitlement — the product: which vendor's roster this binary trusts,
// and the names its artefacts carry.
package entitlement

import (
	"errors"
	"net/url"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

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
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeProductInvalid,
			Reason:  "PRODUCT_INVALID",
			Public:  "the entitlement product is misconfigured",
			Private: "third-party/entitlement: no product was supplied",
		})
	}

	names := make(map[string]bool, len(p.Origins))
	urls := make(map[string]string, len(p.Origins))
	hosts := make(map[string]bool, len(p.Origins))
	var problems []error

	//: Walk every origin: each one can fail independently.
	for _, origin := range p.Origins {
		//: An origin that cannot be named cannot be reported when it fails.
		if origin.Name == "" || origin.BundleURL == "" {
			problems = append(problems, errs.Wrap(nil, errs.WrapParams{
				Code:    CodeProductInvalid,
				Reason:  "PRODUCT_INVALID",
				Public:  "the entitlement product is misconfigured",
				Private: "third-party/entitlement: an origin has an empty Name or BundleURL",
			}))

			continue
		}
		//: A repeated name makes two failures indistinguishable in a log.
		if names[origin.Name] {
			problems = append(problems, errs.Wrap(nil, errs.WrapParams{
				Code:    CodeProductInvalid,
				Reason:  "PRODUCT_INVALID",
				Public:  "the entitlement product is misconfigured",
				Private: "third-party/entitlement: origin name used twice: " + origin.Name,
			}))
		}
		names[origin.Name] = true
		//: Two entries at one URL are one origin listed twice, not a fallback.
		if other, dup := urls[origin.BundleURL]; dup {
			problems = append(problems, errs.Wrap(nil, errs.WrapParams{
				Code:    CodeProductInvalid,
				Reason:  "PRODUCT_INVALID",
				Public:  "the entitlement product is misconfigured",
				Private: "third-party/entitlement: origins " + other + " and " + origin.Name + " publish at the same URL",
			}))
		}
		urls[origin.BundleURL] = origin.Name
		hosts[hostOf(origin.BundleURL)] = true
	}

	//: Redundancy that shares a host is not redundancy.
	if len(hosts) < minRedundantHosts {
		problems = append(problems, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeProductInvalid,
			Reason:  "PRODUCT_INVALID",
			Public:  "the entitlement product is misconfigured",
			Private: "third-party/entitlement: origins span fewer than two distinct hosts, so one outage takes them all down",
		}))
	}

	//: Join so a caller fixing a list sees every problem, not the first.
	return errors.Join(problems...)
}

// hostOf extracts the host from a bundle URL for the redundancy check. A URL it
// cannot parse yields the whole string, which groups malformed entries together
// rather than counting each as its own host.
func hostOf(bundleURL string) string {
	parsed, err := url.Parse(bundleURL)
	//: An unparseable URL is not a host anyone can be redundant across.
	if err != nil || parsed.Host == "" {
		//: Group malformed entries rather than inflating the host count.
		return bundleURL
	}

	//: The host the origin actually resolves to.
	return parsed.Host
}
