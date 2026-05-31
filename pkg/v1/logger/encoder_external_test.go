package logger_test

import (
	"testing"

	logger "github.com/kitsunium/sdk/pkg/v1/logger"
)

func TestNewTextEncoder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		wantName string
	}{
		{"text encoder identifier", "text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc := logger.NewTextEncoder()
			//: a non-nil encoder reporting its canonical name proves wiring.
			if enc == nil {
				t.Fatal("NewTextEncoder returned nil")
			}
			if enc.Name() != tc.wantName {
				t.Errorf("Name = %q, want %q", enc.Name(), tc.wantName)
			}
		})
	}
}

func TestNewJSONEncoder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		wantName string
	}{
		{"json encoder identifier", "json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc := logger.NewJSONEncoder()
			//: a non-nil encoder reporting its canonical name proves wiring.
			if enc == nil {
				t.Fatal("NewJSONEncoder returned nil")
			}
			if enc.Name() != tc.wantName {
				t.Errorf("Name = %q, want %q", enc.Name(), tc.wantName)
			}
		})
	}
}
