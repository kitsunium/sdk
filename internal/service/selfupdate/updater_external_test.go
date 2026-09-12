// Package updater_test provides black-box tests for the updater package.
package selfupdate_test

import (
	"testing"

	selfupdate "github.com/kitsunium/sdk/internal/service/selfupdate"
)

// TestNewService is the canonical KTN-TEST-SYNC test for the exported
// `NewService` constructor. Pins the non-nil contract across a range of
// version strings (release, dev, empty, pre-release).
func TestNewService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "release_version", version: "v1.0.0"},
		{name: "dev_version", version: "dev"},
		{name: "empty_version", version: ""},
		{name: "prerelease_version", version: "v1.0.0-rc.1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := selfupdate.NewService(tc.version, testSource)
			if svc == nil {
				t.Errorf("NewService(%q) = nil, want non-nil", tc.version)
			}
		})
	}
}

// TestNewService_Versions tests updater creation with various versions.
func TestNewService_Versions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{
			name:    "with version",
			version: "v1.0.0",
		},
		{
			name:    "dev version",
			version: "dev",
		},
		{
			name:    "empty version",
			version: "",
		},
		{
			name:    "prerelease version",
			version: "v1.0.0-beta.1",
		},
	}

	// Run each test case
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			updaterInstance := selfupdate.NewService(tt.version, testSource)
			// Check updater is not nil
			if updaterInstance == nil {
				t.Error("NewService() returned nil")
			}
		})
	}
}

// TestNewUpdaterWithDeps tests the constructor with injected dependencies.
func TestNewUpdaterWithDeps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "with version", version: "v1.0.0"},
		{name: "empty version", version: ""},
		{name: "dev version", version: "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Use nil dependencies - will be replaced with defaults
			updaterInstance := selfupdate.NewUpdaterWithDeps(tt.version, testSource, nil, nil, nil)
			// Verify constructor returns non-nil
			if updaterInstance == nil {
				t.Error("NewUpdaterWithDeps() returned nil")
			}
		})
	}
}

// TestService_CheckForUpdate tests that dev builds cannot check for updates.
// Every row in this enumerated error-path table asserts err != nil and the
// CurrentVersion echo, so wantErr is dropped as structurally invariant.
func TestService_CheckForUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		version     string
		wantCurrent string
	}{
		{name: "empty version returns error", version: "", wantCurrent: ""},
		{name: "dev version returns error", version: "dev", wantCurrent: "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			updaterInstance := selfupdate.NewService(tt.version, testSource)
			info, err := updaterInstance.CheckForUpdate()
			if err == nil {
				t.Error("CheckForUpdate() should return error for dev build")
			}
			if info.CurrentVersion != tt.wantCurrent {
				t.Errorf("CurrentVersion = %q, want %q", info.CurrentVersion, tt.wantCurrent)
			}
		})
	}
}

// TestService_Upgrade tests that dev builds cannot upgrade.
// Every row asserts err != nil (the happy path requires real network I/O
// and is covered by TestUpdaterService_Upgrade_withMock).
func TestService_Upgrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "empty version returns error", version: ""},
		{name: "dev version returns error", version: "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			updaterInstance := selfupdate.NewService(tt.version, testSource)
			_, err := updaterInstance.Upgrade()
			if err == nil {
				t.Error("Upgrade() should return error for dev build")
			}
		})
	}
}

// TestNewService_ReturnsNonNil verifies NewService returns a usable instance.
func TestNewService_ReturnsNonNil(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "semver version", version: "v1.0.0"},
		{name: "dev build", version: "dev"},
		{name: "empty version", version: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if svc := selfupdate.NewService(tc.version, testSource); svc == nil {
				t.Errorf("NewService(%q) = nil", tc.version)
			}
		})
	}
}

// TestService_CheckForUpdate_DevBuildErrors verifies dev builds return coreupd.DevBuild.
func TestService_CheckForUpdate_DevBuildErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "empty version", version: ""},
		{name: "dev version", version: "dev"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := selfupdate.NewService(tc.version, testSource)
			_, err := svc.CheckForUpdate()
			if err == nil {
				t.Errorf("CheckForUpdate(%q): expected error, got nil", tc.version)
			}
		})
	}
}

// TestService_Upgrade_DevBuildErrors verifies dev builds cannot upgrade.
func TestService_Upgrade_DevBuildErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "empty version", version: ""},
		{name: "dev version", version: "dev"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := selfupdate.NewService(tc.version, testSource)
			_, err := svc.Upgrade()
			if err == nil {
				t.Errorf("Upgrade(%q): expected error, got nil", tc.version)
			}
		})
	}
}
