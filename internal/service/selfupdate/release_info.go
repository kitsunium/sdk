// Package selfupdate replaces the running binary with a newer signed release.
package selfupdate

// releaseInfo represents the GitHub release API response structure.
// It contains the tag name, metadata, and assets for a release.
type releaseInfo struct {
	TagName    string         `json:"tag_name"`
	Name       string         `json:"name"`
	Prerelease bool           `json:"prerelease"`
	Draft      bool           `json:"draft"`
	CreatedAt  string         `json:"created_at"`
	Assets     []releaseAsset `json:"assets"`
}

// releaseAsset represents a downloadable binary attached to a release.
type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}
