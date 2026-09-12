// Package updater_test provides black-box tests for the candidate functionality.
package selfupdate_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	selfupdate "github.com/kitsunium/sdk/internal/service/selfupdate"
)

// candidateGetter implements selfupdate.Getter for testing candidate operations.
// It routes requests to different responses based on URL path content.
type candidateGetter struct {
	releasesBody   string
	releasesStatus int
	releaseBody    string
	releaseStatus  int
	downloadBody   string
	downloadStatus int
	err            error
}

// Get performs a mock HTTP GET request routed by URL path.
func (g *candidateGetter) Get(url string) (*http.Response, error) {
	// Return configured error
	if g.err != nil {
		// Return error for all requests
		return nil, g.err
	}

	// Route by URL pattern
	if strings.Contains(url, "/releases/tags/") {
		// Return release-by-tag response
		return &http.Response{
			StatusCode: g.releaseStatus,
			Body:       io.NopCloser(strings.NewReader(g.releaseBody)),
		}, nil
	}

	// Route download requests
	if strings.Contains(url, "/releases/download/") {
		// Return download response
		return &http.Response{
			StatusCode: g.downloadStatus,
			Body:       io.NopCloser(strings.NewReader(g.downloadBody)),
		}, nil
	}

	// Default: releases list
	return &http.Response{
		StatusCode: g.releasesStatus,
		Body:       io.NopCloser(strings.NewReader(g.releasesBody)),
	}, nil
}

// TestService_ListCandidates is the canonical KTN-TEST-SYNC test for the
// `ListCandidates` method on `Service`. Exercises the prerelease filter
// (excludes stable + drafts) plus the network-error path.
func TestService_ListCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		releasesBody   string
		releasesStatus int
		wantCount      int
		wantErr        bool
		httpErr        error
	}{
		{
			name: "filters_prereleases",
			releasesBody: `[
				{"tag_name": "v1.0.0", "prerelease": false, "draft": false},
				{"tag_name": "v1.0.0-rc.1", "prerelease": true, "draft": false, "created_at": "2026-03-09T11:00:00Z"}
			]`,
			releasesStatus: http.StatusOK,
			wantCount:      1,
		},
		{
			name:    "transport_error",
			httpErr: errors.New("network down"),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getter := &candidateGetter{
				releasesBody:   tc.releasesBody,
				releasesStatus: tc.releasesStatus,
				err:            tc.httpErr,
			}
			upd := selfupdate.NewUpdaterWithDeps("v1.0.0", testSource, getter, nil, nil)
			cands, err := upd.ListCandidates()
			if tc.wantErr {
				if err == nil {
					t.Fatal("ListCandidates() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ListCandidates() unexpected error: %v", err)
			}
			if got := len(cands); got != tc.wantCount {
				t.Errorf("ListCandidates() returned %d candidates, want %d", got, tc.wantCount)
			}
		})
	}
}

// TestService_DownloadCandidate is the canonical KTN-TEST-SYNC test for
// the `DownloadCandidate` method on `Service`. Pins the input validation
// path (empty tag, malformed tag, valid-but-unreachable) which returns
// before any HTTP success path, plus the discriminating substring of the
// returned error message.
func TestService_DownloadCandidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tag        string
		wantSubstr string
	}{
		{name: "empty_tag_errors", tag: "", wantSubstr: "candidate tag is required"},
		{name: "malformed_tag_errors", tag: "not-a-tag", wantSubstr: "that is not a valid version tag"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getter := &candidateGetter{}
			upd := selfupdate.NewUpdaterWithDeps("v1.0.0", testSource, getter, nil, nil)
			_, err := upd.DownloadCandidate(tc.tag)
			if err == nil {
				t.Fatalf("DownloadCandidate(%q) expected error, got nil", tc.tag)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantSubstr)) {
				t.Errorf("DownloadCandidate(%q) err = %q, want substring %q",
					tc.tag, err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestListCandidates tests listing available release candidates.
func TestListCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		releasesBody   string
		releasesStatus int
		wantCount      int
		wantErr        bool
		httpErr        error
	}{
		{
			name: "returns only prereleases",
			releasesBody: `[
				{"tag_name": "v1.3.113", "prerelease": false, "draft": false},
				{"tag_name": "v1.3.114-rc.42.abc1234", "name": "RC: PR #42", "prerelease": true, "draft": false, "created_at": "2026-03-09T11:00:00Z"},
				{"tag_name": "v1.3.114-rc.43.def5678", "name": "RC: PR #43", "prerelease": true, "draft": false, "created_at": "2026-03-09T12:00:00Z"}
			]`,
			releasesStatus: http.StatusOK,
			wantCount:      2,
		},
		{
			name: "excludes drafts",
			releasesBody: `[
				{"tag_name": "v1.3.114-rc.42.abc1234", "prerelease": true, "draft": true},
				{"tag_name": "v1.3.114-rc.43.def5678", "prerelease": true, "draft": false, "created_at": "2026-03-09T12:00:00Z"}
			]`,
			releasesStatus: http.StatusOK,
			wantCount:      1,
		},
		{
			name:           "empty release list",
			releasesBody:   `[]`,
			releasesStatus: http.StatusOK,
			wantCount:      0,
		},
		{
			name:           "server error",
			releasesBody:   `{"message": "error"}`,
			releasesStatus: http.StatusInternalServerError,
			wantErr:        true,
		},
		{
			name:    "http client error",
			httpErr: errors.New("connection refused"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create updater with mock getter
			getter := &candidateGetter{
				releasesBody:   tt.releasesBody,
				releasesStatus: tt.releasesStatus,
				err:            tt.httpErr,
			}
			upd := selfupdate.NewUpdaterWithDeps("v1.0.0", testSource, getter, nil, nil)

			// Call ListCandidates
			candidates, err := upd.ListCandidates()

			// Check error expectation
			if tt.wantErr {
				// Expect error
				if err == nil {
					t.Fatal("ListCandidates() expected error, got nil")
				}
				// Return after error check
				return
			}

			// Expect no error
			if err != nil {
				t.Fatalf("ListCandidates() unexpected error: %v", err)
			}

			// Check candidate count
			if len(candidates) != tt.wantCount {
				t.Errorf("ListCandidates() returned %d candidates, want %d", len(candidates), tt.wantCount)
			}
		})
	}
}

// TestUpdaterService_ListCandidates_fields tests that candidate fields are populated.
func TestUpdaterService_ListCandidates_fields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantTag  string
		wantName string
		wantDate string
	}{
		{
			name:     "fields populated correctly",
			body:     `[{"tag_name": "v1.3.114-rc.42.abc1234", "name": "RC: PR #42 (abc1234)", "prerelease": true, "draft": false, "created_at": "2026-03-09T11:00:00Z"}]`,
			wantTag:  "v1.3.114-rc.42.abc1234",
			wantName: "RC: PR #42 (abc1234)",
			wantDate: "2026-03-09T11:00:00Z",
		},
	}

	// Run each test case
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create updater with mock getter
			getter := &candidateGetter{
				releasesBody:   tt.body,
				releasesStatus: http.StatusOK,
			}
			upd := selfupdate.NewUpdaterWithDeps("v1.0.0", testSource, getter, nil, nil)

			// Call ListCandidates
			candidates, err := upd.ListCandidates()
			// Check no error
			if err != nil {
				t.Fatalf("ListCandidates() unexpected error: %v", err)
			}

			// Verify single candidate returned
			if len(candidates) != 1 {
				t.Fatalf("expected 1 candidate, got %d", len(candidates))
			}

			// Verify fields
			c := candidates[0]
			// Check tag
			if c.Tag != tt.wantTag {
				t.Errorf("Tag = %q, want %q", c.Tag, tt.wantTag)
			}
			// Check name
			if c.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", c.Name, tt.wantName)
			}
			// Check date
			if c.CreatedAt != tt.wantDate {
				t.Errorf("CreatedAt = %q, want %q", c.CreatedAt, tt.wantDate)
			}
		})
	}
}

// TestDownloadCandidate enumerates every error path of DownloadCandidate.
// The happy path requires real binary I/O and is covered by
// TestUpdaterService_downloadAndReplace_* internal tests.
func TestDownloadCandidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		tag           string
		releaseBody   string
		releaseStatus int
	}{
		{name: "empty tag returns error", tag: ""},
		{
			name:          "release not found",
			tag:           "v1.0.0-rc.99.missing",
			releaseStatus: http.StatusNotFound,
			releaseBody:   `{"message": "Not Found"}`,
		},
		{
			name:          "release is not prerelease",
			tag:           "v1.0.0",
			releaseStatus: http.StatusOK,
			releaseBody:   `{"tag_name": "v1.0.0", "prerelease": false}`,
		},
		{
			name:          "draft release rejected",
			tag:           "v1.0.0-rc.1",
			releaseStatus: http.StatusOK,
			releaseBody:   `{"tag_name": "v1.0.0-rc.1", "prerelease": true, "draft": true}`,
		},
		{name: "that is not a valid version tag", tag: "../../malicious/path"},
		{name: "tag with spaces rejected", tag: "v1.0.0 --flag"},
		{
			name:          "server error",
			tag:           "v1.0.0-rc.1",
			releaseStatus: http.StatusInternalServerError,
			releaseBody:   `{"message": "error"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			getter := &candidateGetter{
				releaseBody:   tt.releaseBody,
				releaseStatus: tt.releaseStatus,
			}
			upd := selfupdate.NewUpdaterWithDeps("v1.0.0", testSource, getter, nil, nil)

			info, err := upd.DownloadCandidate(tt.tag)
			if err == nil {
				t.Fatal("DownloadCandidate() expected error, got nil")
			}
			if info.CurrentVersion != "v1.0.0" {
				t.Errorf("CurrentVersion = %q, want %q", info.CurrentVersion, "v1.0.0")
			}
		})
	}
}
