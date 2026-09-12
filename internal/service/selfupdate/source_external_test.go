// External test fixtures: the release source the black-box suite constructs a
// Service with.
package selfupdate_test

import (
	"testing"

	selfupdate "github.com/kitsunium/sdk/internal/service/selfupdate"
)

// testSource is the SourceValue the external suite injects. It deliberately
// names a product the source implementation never mentioned, so an assertion on
// a built URL or an env-var name fails the moment the parameterisation stops
// being honoured.
var testSource = selfupdate.SourceValue{
	Owner:      "acme",
	StableRepo: "widget-dist",
	DevRepo:    "widget",
	Product:    "widget",
}

// TestEnvNamesAreDerivedNotInvented pins that the derivation reproduces, for a
// product named as the source implementation's binary was, exactly the two
// environment variables that implementation hard-coded.
//
// This is the finding that decided the shape of SourceValue.envPrefix. The
// source hard-coded KTN_LINTER_AUTO_UPGRADE and KTN_LINTER_ALLOW_SUDO, and its
// own suite pinned both names against renaming — "a renamed variable strands
// every environment already setting it". Parameterising them would have broken
// exactly that guarantee if the derivation had been anything other than
// uppercase-and-fold-punctuation. It is not: `ktn-linter` yields `KTN_LINTER`,
// so every CI image and devcontainer already setting those two keeps working.
//
// Change the folding rule and this test fails, which is the point.
func TestEnvNamesAreDerivedNotInvented(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		product       string
		wantAutoUpper string
		wantSudoUpper string
	}{
		{
			name:          "the source implementation's own product",
			product:       "ktn-linter",
			wantAutoUpper: "KTN_LINTER_AUTO_UPGRADE",
			wantSudoUpper: "KTN_LINTER_ALLOW_SUDO",
		},
		{
			name:          "a single word needs no folding",
			product:       "widget",
			wantAutoUpper: "WIDGET_AUTO_UPGRADE",
			wantSudoUpper: "WIDGET_ALLOW_SUDO",
		},
		{
			name:          "dots and slashes fold too, so the name stays reachable",
			product:       "my.tool/v2",
			wantAutoUpper: "MY_TOOL_V2_AUTO_UPGRADE",
			wantSudoUpper: "MY_TOOL_V2_ALLOW_SUDO",
		},
		{
			name:          "an unnamed product gets a generic prefix, never a bare underscore",
			product:       "",
			wantAutoUpper: "SELFUPDATE_AUTO_UPGRADE",
			wantSudoUpper: "SELFUPDATE_ALLOW_SUDO",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := selfupdate.SourceValue{Product: tt.product}
			//: An environment already setting the old name must keep working.
			if got := src.AutoUpgradeEnv(); got != tt.wantAutoUpper {
				t.Errorf("AutoUpgradeEnv() = %q, want %q", got, tt.wantAutoUpper)
			}
			//: Escalation has its own switch and must derive separately.
			if got := src.SudoOptInEnv(); got != tt.wantSudoUpper {
				t.Errorf("SudoOptInEnv() = %q, want %q", got, tt.wantSudoUpper)
			}
			//: The two must never collide, or opting into unattended upgrades
			//: would silently grant privilege escalation as well.
			if src.AutoUpgradeEnv() == src.SudoOptInEnv() {
				t.Errorf("both authorisations read %q", src.AutoUpgradeEnv())
			}
		})
	}
}
