package logger_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

func TestFrameworkVersionFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"returns non-empty sentinel in dev"},
		{"never returns empty string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := logger.FrameworkVersion()
			if got == "" {
				t.Errorf("FrameworkVersion = empty")
			}
		})
	}
}
