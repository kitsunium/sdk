// Package updater provides self-update functionality for ktn-linter binary.
package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Test_getReleases tests fetching all releases from GitHub API.
func Test_getReleases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		responseBody   string
		responseStatus int
		wantCount      int
		wantErr        bool
		wantErrContain string
	}{
		{
			name: "parses multiple releases",
			responseBody: `[
				{"tag_name": "v1.0.0", "prerelease": false},
				{"tag_name": "v1.1.0-rc.1", "prerelease": true}
			]`,
			responseStatus: http.StatusOK,
			wantCount:      2,
			wantErr:        false,
		},
		{
			name:           "empty list",
			responseBody:   `[]`,
			responseStatus: http.StatusOK,
			wantCount:      0,
			wantErr:        false,
		},
		{
			name:           "server error",
			responseBody:   `error`,
			responseStatus: http.StatusInternalServerError,
			wantErr:        true,
			wantErrContain: "the release host answered unexpectedly",
		},
		{
			name:           "invalid json",
			responseBody:   `not json`,
			responseStatus: http.StatusOK,
			wantErr:        true,
			wantErrContain: "parsing releases",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test server
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.responseStatus)
				w.Write([]byte(tt.responseBody))
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			// Create updater with mock transport
			client := &http.Client{
				Transport: &mockTransport{url: server.URL, client: server.Client()},
			}
			updater := NewUpdaterWithDeps("v1.0.0", testSource, client, nil, nil)

			// Call getReleases
			releases, err := updater.getReleases()

			// Check error expectation
			if tt.wantErr {
				// Expect error
				if err == nil {
					t.Fatal("getReleases() expected error, got nil")
				}
				// Check error message
				if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErrContain)
				}
				// Return after error check
				return
			}

			// Expect no error
			if err != nil {
				t.Fatalf("getReleases() unexpected error: %v", err)
			}
			// Check release count
			if len(releases) != tt.wantCount {
				t.Errorf("getReleases() returned %d releases, want %d", len(releases), tt.wantCount)
			}
		})
	}
}

// Test_getReleaseByTag tests fetching a release by its tag.
func Test_getReleaseByTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		tag            string
		responseBody   string
		responseStatus int
		wantTag        string
		wantErr        bool
		wantErrContain string
	}{
		{
			name:           "successful fetch",
			tag:            "v1.0.0-rc.1",
			responseBody:   `{"tag_name": "v1.0.0-rc.1", "prerelease": true, "name": "RC 1"}`,
			responseStatus: http.StatusOK,
			wantTag:        "v1.0.0-rc.1",
			wantErr:        false,
		},
		{
			name:           "not found",
			tag:            "v99.0.0-rc.1",
			responseBody:   `{"message": "Not Found"}`,
			responseStatus: http.StatusNotFound,
			wantErr:        true,
			wantErrContain: "no such release candidate",
		},
		{
			name:           "server error",
			tag:            "v1.0.0-rc.1",
			responseBody:   `error`,
			responseStatus: http.StatusInternalServerError,
			wantErr:        true,
			wantErrContain: "the release host answered unexpectedly",
		},
		{
			name:           "invalid json",
			tag:            "v1.0.0-rc.1",
			responseBody:   `not json`,
			responseStatus: http.StatusOK,
			wantErr:        true,
			wantErrContain: "parsing release",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test server
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.responseStatus)
				w.Write([]byte(tt.responseBody))
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			// Create updater with mock transport
			client := &http.Client{
				Transport: &mockTransport{url: server.URL, client: server.Client()},
			}
			updater := NewUpdaterWithDeps("v1.0.0", testSource, client, nil, nil)

			// Call getReleaseByTag
			release, err := updater.getReleaseByTag(tt.tag)

			// Check error expectation
			if tt.wantErr {
				// Expect error
				if err == nil {
					t.Fatal("getReleaseByTag() expected error, got nil")
				}
				// Check error message
				if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErrContain)
				}
				// Return after error check
				return
			}

			// Expect no error
			if err != nil {
				t.Fatalf("getReleaseByTag() unexpected error: %v", err)
			}
			// Check tag name
			if release.TagName != tt.wantTag {
				t.Errorf("TagName = %q, want %q", release.TagName, tt.wantTag)
			}
		})
	}
}
