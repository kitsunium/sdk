package net_test

import (
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestAcceptsEncoding pins the field grammar and its bounds.
func TestAcceptsEncoding(t *testing.T) {
	for _, tc := range []struct {
		value, coding string
		want          bool
	}{
		{"gzip", "gzip", true},
		{"GZip", "gzip", true},
		{"gzip;q=0", "gzip", false},
		{"gzip;q=0.5", "gzip", true},
		{"gzip ; q=0.000", "gzip", false},
		{"*", "gzip", true},
		{"*;q=0", "gzip", false},
		{"*, gzip;q=0", "gzip", false},
		{"gzip;q=0, *", "gzip", false},
		{"identity", "gzip", false},
		{"", "gzip", false},
		{"gzip;q=x", "gzip", false},
		{strings.Repeat("a,", 600) + "gzip", "gzip", false},
		{strings.Repeat("a", 2000) + ",gzip", "gzip", false},
	} {
		if got := corenet.AcceptsEncoding(tc.value, tc.coding); got != tc.want {
			t.Errorf("AcceptsEncoding(%q, %q) = %v, want %v", tc.value, tc.coding, got, tc.want)
		}
	}
}
