// Package selfupdate replaces the running binary with a newer signed release.
package selfupdate

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// tagPattern validates that a tag contains only safe characters for URL construction.
var tagPattern *regexp.Regexp = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9-]+(\.[a-zA-Z0-9-]+)*)?$`)

// CandidateValue represents one available release candidate.
// It contains the tag, display name, and creation timestamp from GitHub.
type CandidateValue struct {
	Tag       string
	Name      string
	CreatedAt string
}

// ListCandidates returns all available release candidates.
func (u *Service) ListCandidates() (candidates []CandidateValue, listErr error) {
	// Fetch all releases from GitHub API
	releases, err := u.getReleases()
	//: Propagate API errors to caller for handling.
	if err != nil {
		//: Return error as-is to preserve context.
		return nil, err
	}

	// Filter to only pre-releases that are not drafts
	//: Loop through all releases from the API.
	for _, r := range releases {
		//: Only include released RC versions, not drafts or stable releases.
		if !r.Prerelease || r.Draft {
			continue
		}

		// Append to candidates list
		candidates = append(candidates, CandidateValue{
			Tag:       r.TagName,
			Name:      r.Name,
			CreatedAt: r.CreatedAt,
		})
	}

	//: Return filtered RC list (may be empty if no candidates exist).
	return candidates, nil
}

// DownloadCandidate downloads and installs a specific release candidate.
func (u *Service) DownloadCandidate(tag string) (info UpdateValue, downloadErr error) {
	//: Prevent empty tag from reaching API.
	if tag == "" {
		//: Return error to signal missing parameter.
		return UpdateValue{CurrentVersion: u.version}, fmt.Errorf("%w: candidate tag is required", errors.ErrUnsupported)
	}

	//: Validate tag format before constructing URLs to prevent injection attacks.
	if !tagPattern.MatchString(tag) {
		//: Reject malformed tags to prevent URL construction errors.
		return UpdateValue{CurrentVersion: u.version}, fmt.Errorf("%w: %s", coreupd.InvalidTag, tag)
	}

	// Verify the release exists and is a prerelease
	release, err := u.getReleaseByTag(tag)
	//: Propagate API errors to caller.
	if err != nil {
		//: Return error directly from API.
		return UpdateValue{CurrentVersion: u.version}, err
	}

	//: Match ListCandidates filtering to prevent inconsistent behavior.
	if release.Draft {
		//: Reject draft to prevent unstable release installation.
		return UpdateValue{CurrentVersion: u.version}, fmt.Errorf("%w: %s", coreupd.DraftRelease, tag)
	}

	//: Confirm this is actually a prerelease, not stable.
	if !release.Prerelease {
		//: Reject stable release to prevent mixing with RC flow.
		return UpdateValue{CurrentVersion: u.version}, fmt.Errorf("%w: %s", coreupd.NotPrerelease, tag)
	}

	// Download and replace binary with candidate version
	err = u.downloadAndReplace(tag)
	//: Propagate download errors to caller.
	if err != nil {
		//: Return error from download/replace operation.
		return UpdateValue{CurrentVersion: u.version}, err
	}

	//: Return successful installation info.
	return UpdateValue{
		Available:      true,
		CurrentVersion: u.version,
		LatestVersion:  tag,
	}, nil
}

// getReleases fetches all releases from the GitHub API.
func (u *Service) getReleases() (releases []releaseInfo, getErr error) {
	// Build API URL for all releases
	url := fmt.Sprintf(releasesURL, u.src.Owner, u.src.candidateRepo())
	// Make HTTP request
	resp, err := u.client.Get(url)
	//: Propagate network errors to caller.
	if err != nil {
		//: Wrap error to add context about the operation.
		return nil, fmt.Errorf("%w: fetching releases: %w", coreupd.DownloadFailed, err)
	}
	defer func() {
		//: Prevent resource leaks from unclosed response body.
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("close response body: %v", cerr)
		}
	}()

	//: Fail fast on HTTP errors before attempting parse.
	if resp.StatusCode != http.StatusOK {
		//: Return error indicating API request failure.
		return nil, fmt.Errorf("%w: %d", coreupd.UnexpectedStatus, resp.StatusCode)
	}

	// Parse JSON response under a read cap — the /releases page is a few
	// hundred kilobytes and an unbounded decoder let the endpoint choose.
	//: Decode response body into release structures.
	if err := decodeJSONBody(resp.Body, maxAPIBodyBytes, &releases); err != nil {
		//: Wrap error to indicate parsing failure.
		return nil, fmt.Errorf("parsing releases: %w", err)
	}

	//: Return API response to caller.
	return releases, nil
}

// getReleaseByTag fetches a specific release by its tag name.
func (u *Service) getReleaseByTag(tag string) (release releaseInfo, getErr error) {
	// Build API URL for specific tag
	url := fmt.Sprintf(releaseByTagURL, u.src.Owner, u.src.candidateRepo(), tag)
	// Make HTTP request
	resp, err := u.client.Get(url)
	//: Propagate network errors to caller.
	if err != nil {
		//: Wrap error with context about which tag was requested.
		return releaseInfo{}, fmt.Errorf("%w: fetching release %s: %w", coreupd.DownloadFailed, tag, err)
	}
	defer func() {
		//: Prevent resource leak from unclosed body.
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("close response body: %v", cerr)
		}
	}()

	//: Distinguish 404 from other errors for clearer messaging.
	if resp.StatusCode == http.StatusNotFound {
		//: Return specific error when release does not exist.
		return releaseInfo{}, fmt.Errorf("%w: %s", coreupd.CandidateNotFound, tag)
	}

	//: Fail fast on HTTP errors before parsing.
	if resp.StatusCode != http.StatusOK {
		//: Return error indicating API request failure.
		return releaseInfo{}, fmt.Errorf("%w: %d", coreupd.UnexpectedStatus, resp.StatusCode)
	}

	// Parse JSON response under a read cap (see getReleases).
	//: Decode response body into release structure.
	if err := decodeJSONBody(resp.Body, maxAPIBodyBytes, &release); err != nil {
		//: Wrap error with context about which tag failed parsing.
		return releaseInfo{}, fmt.Errorf("parsing release %s: %w", tag, err)
	}

	//: Return API response to caller.
	return release, nil
}
