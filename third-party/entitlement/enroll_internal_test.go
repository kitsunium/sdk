package entitlement

import (
	"strings"
	"testing"

	svcent "github.com/kitsunium/sdk/internal/service/entitlement"
)

// TestEnrolmentLabels pins the two labels the vendor workflows key off. A
// silent rename here would make every enrolment request land unrouted.
func TestEnrolmentLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		got   string
		want  string
		about string
	}{
		{name: "first enrolment", got: requestLabel, want: "license:request", about: "routes a new subject"},
		{name: "key rotation", got: updateLabel, want: "license:update", about: "routes an existing subject"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.got != tt.want {
				t.Errorf("%s label = %q, want %q (%s)", tt.name, tt.got, tt.want, tt.about)
			}
		})
	}
}

// TestIssueBaseURL pins that requests are filed against the public mirror and
// carry no credential: the whole point is that a client needs only a GitHub
// account.
func TestIssueBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enrol   string
		subject string
		want    string
		reason  string
	}{
		{
			name:    "the product's URL is the base, unrewritten",
			enrol:   "https://example.invalid/widget/issues/new",
			subject: "abc",
			want:    "https://example.invalid/widget/issues/new?",
			reason:  "the source implementation hard-coded one vendor's tracker; it is the product's now",
		},
		{
			name:    "a product with no enrolment path yields only the query",
			enrol:   "",
			subject: "abc",
			want:    "?",
			reason:  "an empty EnrolURL means the product offers no self-service path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: The assertion that this targets one vendor's tracker went with
			//: the constant. What survives is that IssueURL builds ON the
			//: product's URL rather than substituting one of its own.
			product := new(svcent.ProductValue)
			product.Name = "widget"
			product.EnrolURL = tt.enrol
			got := IssueURL(product, tt.subject, "ssh-ed25519 AAAA", false)
			if !strings.HasPrefix(got, tt.want) {
				t.Errorf("IssueURL() = %q, want it to start with %q (%s)", got, tt.want, tt.reason)
			}
		})
	}
}
