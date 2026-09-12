// Package selfupdate replaces the running binary with a newer signed release.
package selfupdate

// UpdateValue contains comprehensive information about available updates.
// It provides the current version, latest available version, and availability status.
type UpdateValue struct {
	Available      bool
	CurrentVersion string
	LatestVersion  string
}
