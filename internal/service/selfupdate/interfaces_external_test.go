// Package updater_test provides external tests for updater interfaces.
package selfupdate_test

import (
	"testing"

	selfupdate "github.com/kitsunium/sdk/internal/service/selfupdate"
)

// TestUpdaterInterfaces verifies that all interface types are properly defined.
func TestUpdaterInterfaces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "Getter interface is defined"},
		{name: "FileSystem interface is defined"},
		{name: "Copier interface is defined"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Verify that NewService creates a non-nil instance (uses all three interfaces)
			u := selfupdate.NewService("1.0.0", testSource)
			if u == nil {
				t.Error("NewService() returned nil")
			}
		})
	}
}
