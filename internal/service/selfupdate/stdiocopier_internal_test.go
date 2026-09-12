// Package updater provides self-update functionality for ktn-linter binary.
package selfupdate

import (
	"bytes"
	"testing"
)

// TestStdIOCopier_Copy tests the Copy method.
func TestStdIOCopier_Copy(t *testing.T) {
	t.Parallel()
	// Define test cases
	tests := []struct {
		name     string
		srcData  string
		expected string
	}{
		{
			name:     "copy test data",
			srcData:  "test data",
			expected: "test data",
		},
	}

	// Run tests
	for _, tt := range tests {
		// Run subtest
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			copier := stdIOCopier{}
			// Prepare source data
			src := bytes.NewBufferString(tt.srcData)
			dst := &bytes.Buffer{}

			// Copy data
			n, err := copier.Copy(dst, src)
			// Check for errors
			if err != nil {
				t.Fatalf("Copy returned error: %v", err)
			}
			// Verify number of bytes copied
			if n != int64(len(tt.expected)) {
				t.Errorf("expected %d bytes copied, got %d", len(tt.expected), n)
			}
			// Verify content
			if dst.String() != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, dst.String())
			}
		})
	}
}
